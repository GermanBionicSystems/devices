// Copyright 2017 The Periph Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package lepton

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"github.com/GermanBionicSystems/devices/v3/lepton/cci"
	"github.com/GermanBionicSystems/devices/v3/lepton/image14bit"
	"github.com/GermanBionicSystems/devices/v3/lepton/internal"
)

// Metadata is constructed from telemetry data, which is sent with each frame.
type Metadata struct {
	SinceStartup   time.Duration      //
	FrameCount     uint32             // Number of frames since the start of the camera, in 27fps (not 9fps).
	AvgValue       uint16             // Average value of the buffer.
	Temp           physic.Temperature // Temperature inside the camera.
	TempHousing    physic.Temperature // Camera housing temperature.
	RawTemp        uint16             //
	RawTempHousing uint16             //
	FFCSince       time.Duration      // Time since last internal calibration.
	FFCTemp        physic.Temperature // Temperature at last internal calibration.
	FFCTempHousing physic.Temperature //
	FFCState       cci.FFCState       // Current calibration state.
	FFCDesired     bool               // Asserted at start-up, after period (default 3m) or after temperature change (default 3K). Indicates that a calibration should be triggered as soon as possible.
	Overtemp       bool               // true 10s before self-shutdown.
}

// Frame is a FLIR Lepton frame, containing 14 bits resolution intensity stored
// as image14bit.Gray14.
//
// Values centered around 8192 accorging to camera body temperature. Effective
// range is 14 bits, so [0, 16383].
//
// Each 1 increment is approximatively 0.025K.
type Frame struct {
	*image14bit.Gray14
	Metadata Metadata // Metadata that is sent along the pixels.
}

// New returns an initialized connection to the FLIR Lepton.
//
// Maximum SPI speed is 20Mhz. Minimum usable rate is ~2.2Mhz to sustain a 9hz
// framerate at 80x60.
//
// Maximum I²C speed is 1Mhz.
//
// MOSI is not used and should be grounded.
func New(p spi.Port, i i2c.Bus) (*Dev, error) {
	// TODO(maruel): Switch to 16 bits per word, so that big endian 16 bits word
	// decoding is done by the SPI driver.
	s, err := p.Connect(20*physic.MegaHertz, spi.Mode3, 8)
	if err != nil {
		return nil, err
	}
	c, err := cci.New(i)
	if err != nil {
		return nil, err
	}
	// TODO(maruel): Support Lepton 3 with 160x120.
	w := 80
	h := 60
	// telemetry data is a 3 lines header.
	frameLines := h + 3
	frameWidth := w*2 + 4
	d := &Dev{
		Dev:        c,
		s:          s,
		w:          w,
		h:          h,
		prevImg:    image14bit.NewGray14(image.Rect(0, 0, w, h)),
		frameWidth: frameWidth,
		frameLines: frameLines,
		delay:      time.Second,
	}
	if l, ok := s.(conn.Limits); ok {
		d.maxTxSize = l.MaxTxSize()
	}
	if status, err := d.GetStatus(); err != nil {
		return nil, err
	} else if status.CameraStatus != cci.SystemReady {
		// The lepton takes < 1 second to boot so it should not happen normally.
		return nil, fmt.Errorf("lepton: camera is not ready: %#v", status)
	}
	if err := d.Init(); err != nil {
		return nil, err
	}
	return d, nil
}

// Dev controls a FLIR Lepton.
//
// It assumes a specific breakout board. Sadly the breakout board doesn't
// expose the PWR_DWN_L and RESET_L lines so it is impossible to shut down the
// Lepton.
type Dev struct {
	*cci.Dev
	s          spi.Conn
	w          int
	h          int
	prevImg    *image14bit.Gray14
	frameWidth int // in bytes
	frameLines int
	maxTxSize  int
	delay      time.Duration
}

func (d *Dev) String() string {
	return fmt.Sprintf("Lepton(%s/%s)", d.Dev, d.s)
}

// Halt implements conn.Resource.
func (d *Dev) Halt() error {
	// TODO(maruel): Stop the read loop.
	return d.Dev.Halt()
}

// Bounds returns the device frame size.
func (d *Dev) Bounds() image.Rectangle {
	return image.Rect(0, 0, d.w, d.h)
}

// NextFrame blocks and returns the next frame from the camera.
//
// It is ok to call other functions concurrently to send commands to the
// camera.
func (d *Dev) NextFrame(f *Frame) error {
	if f.Bounds() != d.Bounds() {
		return errors.New("lepton: invalid frame size")
	}
	for {
		if err := d.readFrame(f); err != nil {
			return err
		}
		/*
			if f.Metadata.FFCDesired {
				// TODO(maruel): Automatically trigger FFC when applicable, only do if
				// the camera has a shutter.
				go d.RunFFC()
			}
		*/
		// Sadly the Lepton will unconditionally send 27fps, even if the effective
		// rate is 9fps.
		if !equalUint16(d.prevImg.Pix, f.Gray14.Pix) {
			break
		}
		// It also happen if the image is 100% static without noise.
	}
	copy(d.prevImg.Pix, f.Pix)
	return nil
}

// Private details.

// stream reads continuously from the SPI connection.
func (d *Dev) stream(done <-chan struct{}, c chan<- []byte) error {
	lines := 8
	if d.maxTxSize != 0 {
		if l := d.maxTxSize / d.frameWidth; l < lines {
			lines = l
		}
	}
	for {
		// TODO(maruel): Use a ring buffer to stop continuously allocating.
		buf := make([]byte, d.frameWidth*lines)
		if err := d.s.Tx(nil, buf); err != nil {
			return err
		}
		for i := 0; i < len(buf); i += d.frameWidth {
			select {
			case <-done:
				return nil
			case c <- buf[i : i+d.frameWidth]:
			}
		}
	}
}

// readFrame reads one frame.
//
// Each frame is sent as a packet over SPI including telemetry data as an
// header. See page 49-57 for "VoSPI" protocol explanation.
//
// This operation must complete within 32ms. Frames occur every 38.4ms at
// almost 27hz.
//
// Resynchronization is done by deasserting CS and CLK for at least 5 frames
// (>185ms).
//
// When a packet starts, it must be completely clocked out within 3 line
// periods.
//
// One frame of 80x60 at 2 byte per pixel, plus 4 bytes overhead per line plus
// 3 lines of telemetry is (3+60)*(4+160) = 10332. The sysfs-spi driver limits
// each transaction size, the default is 4Kb. To reduce the risks of failure,
// reads 4Kb at a time and figure out the lines from there. The Lepton is very
// cranky if reading is not done quickly enough.
func (d *Dev) readFrame(f *Frame) error {
	done := make(chan struct{}, 1)
	c := make(chan []byte, 1024)
	var err error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(c)
		err = d.stream(done, c)
	}()
	defer func() {
		done <- struct{}{}
	}()

	timeout := time.After(d.delay)
	w := f.Bounds().Dx()
	sync := 0
	discard := 0
	for {
		select {
		case <-timeout:
			return fmt.Errorf("failed to synchronize after %s", d.delay)
		case l, ok := <-c:
			if !ok {
				wg.Wait()
				return err
			}
			h := internal.Big16.Uint16(l)
			if h&packetHeaderDiscard == packetHeaderDiscard {
				discard++
				sync = 0
				continue
			}
			headerID := h & packetHeaderMask
			if discard != 0 {
				//log.Printf("discarded %d", discard)
				discard = 0
				sync = 0
			}
			if int(headerID) == 0 && sync == 0 && !verifyCRC(l) {
				//log.Printf("no crc")
				sync = 0
				continue
			}
			if int(headerID) != sync {
				//log.Printf("%d != %d", headerID, sync)
				sync = 0
				continue
			}
			if sync == 0 {
				// Parse the first row of telemetry data.
				if err2 := f.Metadata.parseTelemetry(l[4:]); err2 != nil {
					//log.Printf("Failed to parse telemetry line: %v", err2)
					continue
				}
			} else if sync >= 3 {
				// Image.
				for x := 0; x < w; x++ {
					o := 4 + x*2
					f.SetIntensity14(x, sync-3, image14bit.Intensity14(internal.Big16.Uint16(l[o:o+2])))
				}
			}
			if sync++; sync == d.frameLines {
				// Last line, done.
				return nil
			}
		}
	}
}

func (m *Metadata) parseTelemetry(data []byte) error {
	// Telemetry line.
	var rowA telemetryRowA
	if err := binary.Read(bytes.NewBuffer(data), internal.Big16, &rowA); err != nil {
		return err
	}
	m.SinceStartup = rowA.TimeCounter.Duration()
	m.FrameCount = rowA.FrameCounter
	m.AvgValue = rowA.FrameMean
	m.Temp = rowA.FPATemp.Temperature()
	m.TempHousing = rowA.HousingTemp.Temperature()
	m.RawTemp = rowA.FPATempCounts
	m.RawTempHousing = rowA.HousingTempCounts
	m.FFCSince = rowA.TimeCounterLastFFC.Duration()
	m.FFCTemp = rowA.FPATempLastFFC.Temperature()
	m.FFCTempHousing = rowA.HousingTempLastFFC.Temperature()
	if rowA.StatusBits&statusMaskNil != 0 {
		return fmt.Errorf("lepton: (Status: 0x%08X) & (Mask: 0x%08X) = (Extra: 0x%08X) in 0x%08X", rowA.StatusBits, statusMask, rowA.StatusBits&statusMaskNil, statusMaskNil)
	}
	m.FFCDesired = rowA.StatusBits&statusFFCDesired != 0
	m.Overtemp = rowA.StatusBits&statusOvertemp != 0
	fccstate := rowA.StatusBits & statusFFCStateMask >> statusFFCStateShift
	if rowA.TelemetryRevision == 8 {
		switch fccstate {
		case 0:
			m.FFCState = cci.FFCNever
		case 1:
			m.FFCState = cci.FFCInProgress
		case 2:
			m.FFCState = cci.FFCComplete
		default:
			return fmt.Errorf("unexpected fccstate %d; %v", fccstate, data)
		}
	} else {
		switch fccstate {
		case 0:
			m.FFCState = cci.FFCNever
		case 2:
			m.FFCState = cci.FFCInProgress
		case 3:
			m.FFCState = cci.FFCComplete
		default:
			return fmt.Errorf("unexpected fccstate %d; %v", fccstate, data)
		}
	}
	return nil
}

// As documented as page.21
const (
	packetHeaderDiscard = 0x0F00
	packetHeaderMask    = 0x0FFF // ID field is 12 bits. Leading 4 bits are reserved.
	// Observed status:
	//   0x00000808
	//   0x00007A01
	//   0x00022200
	//   0x01AD0000
	//   0x02BF0000
	//   0x1FFF0000
	//   0x3FFF0001
	//   0xDCD0FFFF
	//   0xFFDCFFFF
	statusFFCDesired    uint32 = 1 << 3                                                                                   // 0x00000008
	statusFFCStateMask  uint32 = 3 << 4                                                                                   // 0x00000030
	statusFFCStateShift uint32 = 4                                                                                        //
	statusReserved      uint32 = 1 << 11                                                                                  // 0x00000800
	statusAGCState      uint32 = 1 << 12                                                                                  // 0x00001000
	statusOvertemp      uint32 = 1 << 20                                                                                  // 0x00100000
	statusMask                 = statusFFCDesired | statusFFCStateMask | statusAGCState | statusOvertemp | statusReserved // 0x00101838
	statusMaskNil              = ^statusMask                                                                              // 0xFFEFE7C7
)

// telemetryRowA is the data structure returned after the frame as documented
// at p.19-20.
//
// '*' means the value observed in practice make sense.
// Value after '-' is observed value.
type telemetryRowA struct {
	TelemetryRevision  uint16              // 0  *
	TimeCounter        internal.DurationMS // 1  *
	StatusBits         uint32              // 3  * Bit field (mostly make sense)
	ModuleSerial       [16]uint8           // 5  - Is empty (!)
	SoftwareRevision   uint64              // 13   Junk.
	Reserved17         uint16              // 17 - 1101
	Reserved18         uint16              // 18
	Reserved19         uint16              // 19
	FrameCounter       uint32              // 20 *
	FrameMean          uint16              // 22 * The average value from the whole frame.
	FPATempCounts      uint16              // 23
	FPATemp            internal.CentiK     // 24 *
	HousingTempCounts  uint16              // 25
	HousingTemp        internal.CentiK     // 27 *
	Reserved27         uint16              // 27
	Reserved28         uint16              // 28
	FPATempLastFFC     internal.CentiK     // 29 *
	TimeCounterLastFFC internal.DurationMS // 30 *
	HousingTempLastFFC internal.CentiK     // 32 *
	Reserved33         uint16              // 33
	AGCROILeft         uint16              // 35 * - 0 (Likely inversed, haven't confirmed)
	AGCROITop          uint16              // 34 * - 0
	AGCROIRight        uint16              // 36 * - 79 - SDK was wrong!
	AGCROIBottom       uint16              // 37 * - 59 - SDK was wrong!
	AGCClipLimitHigh   uint16              // 38 *
	AGCClipLimitLow    uint16              // 39 *
	Reserved40         uint16              // 40 - 1
	Reserved41         uint16              // 41 - 128
	Reserved42         uint16              // 42 - 64
	Reserved43         uint16              // 43
	Reserved44         uint16              // 44
	Reserved45         uint16              // 45
	Reserved46         uint16              // 46
	Reserved47         uint16              // 47 - 1
	Reserved48         uint16              // 48 - 128
	Reserved49         uint16              // 49 - 1
	Reserved50         uint16              // 50
	Reserved51         uint16              // 51
	Reserved52         uint16              // 52
	Reserved53         uint16              // 53
	Reserved54         uint16              // 54
	Reserved55         uint16              // 55
	Reserved56         uint16              // 56 - 30
	Reserved57         uint16              // 57
	Reserved58         uint16              // 58 - 1
	Reserved59         uint16              // 59 - 1
	Reserved60         uint16              // 60 - 78
	Reserved61         uint16              // 61 - 58
	Reserved62         uint16              // 62 - 7
	Reserved63         uint16              // 63 - 90
	Reserved64         uint16              // 64 - 40
	Reserved65         uint16              // 65 - 210
	Reserved66         uint16              // 66 - 255
	Reserved67         uint16              // 67 - 255
	Reserved68         uint16              // 68 - 23
	Reserved69         uint16              // 69 - 6
	Reserved70         uint16              // 70
	Reserved71         uint16              // 71
	Reserved72         uint16              // 72 - 7
	Reserved73         uint16              // 73
	Log2FFCFrames      uint16              // 74 Found 3, should be 27?
	Reserved75         uint16              // 75
	Reserved76         uint16              // 76
	Reserved77         uint16              // 77
	Reserved78         uint16              // 78
	Reserved79         uint16              // 79
}

// verifyCRC test the equation x^16 + x^12 + x^5 + x^0
func verifyCRC(d []byte) bool {
	tmp := make([]byte, len(d))
	copy(tmp, d)
	tmp[0] &^= 0x0F
	tmp[2] = 0
	tmp[3] = 0
	return internal.CRC16(tmp) == internal.Big16.Uint16(d[2:])
}

func equalUint16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ conn.Resource = &Dev{}
