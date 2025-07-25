// Copyright 2017 The Periph Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package lepton

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/conntest"
	"periph.io/x/conn/v3/i2c/i2ctest"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spitest"
	"github.com/GermanBionicSystems/devices/v3/lepton/image14bit"
	"github.com/GermanBionicSystems/devices/v3/lepton/internal"
)

func TestNew_cs(t *testing.T) {
	i := i2ctest.Playback{
		Ops: append(initSequence(),
			[]i2ctest.IO{
				{Addr: 42, W: []byte{0x0, 0x2}, R: []byte{0x0, 0x6}}, // waitIdle
				{Addr: 42, W: []byte{0x0, 0x6, 0x0, 0x0}},
				{Addr: 42, W: []byte{0x0, 0x4, 0x48, 0x2}},
				{Addr: 42, W: []byte{0x0, 0x2}, R: []byte{0x0, 0x6}}, // waitIdle
			}...),
	}
	s := spitest.Playback{}
	d, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	if s := d.String(); s != "Lepton(playback(42)/playback)" {
		t.Fatal(s)
	}
	if err := d.Halt(); err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNew(t *testing.T) {
	i := i2ctest.Playback{Ops: initSequence()}
	s := spitest.Playback{}
	_, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNew_Init_fail(t *testing.T) {
	// Strip off last command.
	ops := initSequence()
	i := i2ctest.Playback{Ops: ops[:len(ops)-1], DontPanic: true}
	s := spitest.Playback{}
	if _, err := New(&s, &i); err == nil {
		t.Fatal("cci.Dev.Init() failed")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNew_GetStatus_fail(t *testing.T) {
	i := i2ctest.Playback{
		Ops: []i2ctest.IO{
			{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}}, // waitIdle
			{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}}, // waitIdle
			{Addr: 42, W: []byte{0, 6, 0, 4}},            // GetStatus()
			{Addr: 42, W: []byte{0, 4, 2, 4}},            //
			{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}}, // waitIdle
		},
		DontPanic: true,
	}
	s := spitest.Playback{}
	if _, err := New(&s, &i); err == nil {
		t.Fatal("cci.Dev.GetStatus() failed")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNew_GetStatus_bad(t *testing.T) {
	i := i2ctest.Playback{
		Ops: []i2ctest.IO{
			{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
			{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
			{Addr: 42, W: []byte{0, 6, 0, 4}},                              // GetStatus()
			{Addr: 42, W: []byte{0, 4, 2, 4}},                              //
			{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
			{Addr: 42, W: []byte{0, 8}, R: []byte{1, 0, 0, 0, 0, 0, 0, 0}}, // GetStatus() result
		},
		DontPanic: true,
	}
	s := spitest.Playback{}
	if _, err := New(&s, &i); err == nil {
		t.Fatal("cci.Dev.GetStatus() failed")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNew_Connect(t *testing.T) {
	i := i2ctest.Record{}
	s := spiStream{err: errors.New("injected")}
	if _, err := New(&s, &i); err == nil {
		t.Fatal("Connect failed")
	}
}

func TestNew_cci_New_fail(t *testing.T) {
	i := i2ctest.Playback{DontPanic: true}
	s := spitest.Record{}
	if _, err := New(&s, &i); err == nil {
		t.Fatal("cci.New failed")
	}
}

func TestBounds(t *testing.T) {
	i := i2ctest.Playback{Ops: initSequence()}
	s := spitest.Playback{}
	d, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	if r := d.Bounds(); r != image.Rect(0, 0, 80, 60) {
		t.Fatal(r)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNextFrame(t *testing.T) {
	i := i2ctest.Playback{Ops: initSequence()}
	s := spiStream{data: prepareFrame(t)}
	d, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	f := Frame{Gray14: image14bit.NewGray14(d.Bounds())}
	if err := d.NextFrame(&f); err != nil {
		t.Fatal(err)
	}
	if f.Metadata.TempHousing != 2*physic.Celsius+physic.ZeroCelsius {
		t.Fatal(f.Metadata.TempHousing)
	}
	// Compare the frame with the reference image. It should match.
	ref := referenceFrame()
	if !equalUint16(ref.Pix, f.Pix) {
		offset := 0
		for {
			if ref.Pix[offset] != f.Pix[offset] {
				break
			}
			offset++
		}
		t.Fatalf("different pixels at offset %d", offset)
		//t.Fatalf("different pixels at offset %d:\n%x\n%x", offset, ref.Pix[offset:], f.Pix[offset:])
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNextFrame_invalid_bounds(t *testing.T) {
	i := i2ctest.Playback{Ops: initSequence()}
	s := spiStream{data: prepareFrame(t)}
	d, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.NextFrame(&Frame{Gray14: image14bit.NewGray14(image.Rect(0, 0, 1, 1))}); err == nil {
		t.Fatal("invalid bounds")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNextFrame_fail_Tx(t *testing.T) {
	i := i2ctest.Playback{Ops: initSequence()}
	s := spitest.Playback{Playback: conntest.Playback{DontPanic: true}}
	d, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.NextFrame(&Frame{Gray14: image14bit.NewGray14(d.Bounds())}); err == nil {
		t.Fatal("spi port Tx failed")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNextFrame_fail_Out(t *testing.T) {
	i := i2ctest.Playback{Ops: initSequence()}
	s := spitest.Playback{Playback: conntest.Playback{DontPanic: true}}
	d, err := New(&s, &i)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.NextFrame(&Frame{Gray14: image14bit.NewGray14(d.Bounds())}); err == nil {
		t.Fatal("spi port Tx failed")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestParseTelemetry_fail(t *testing.T) {
	l := telemetryLine(t)
	m := Metadata{}
	if m.parseTelemetry(l[:len(l)-1]) == nil {
		t.Fatal("buffer too short")
	}
	buf := bytes.Buffer{}
	rowA := telemetryRowA{StatusBits: statusMaskNil}
	if err := binary.Write(&buf, internal.Big16, &rowA); err != nil {
		t.Fatal(err)
	}
	if m.parseTelemetry(buf.Bytes()) == nil {
		t.Fatal("bad status")
	}
}

func TestParseTelemetry(t *testing.T) {
	m := Metadata{}
	if err := m.parseTelemetry(telemetryLine(t)); err != nil {
		t.Fatal(err)
	}

	data := []struct {
		rowA    telemetryRowA
		success bool
	}{
		{telemetryRowA{TelemetryRevision: 8, StatusBits: 0 << statusFFCStateShift}, true},
		{telemetryRowA{TelemetryRevision: 8, StatusBits: 1 << statusFFCStateShift}, true},
		{telemetryRowA{TelemetryRevision: 8, StatusBits: 2 << statusFFCStateShift}, true},
		{telemetryRowA{TelemetryRevision: 8, StatusBits: 3 << statusFFCStateShift}, false},
		{telemetryRowA{StatusBits: 0 << statusFFCStateShift}, true},
		{telemetryRowA{StatusBits: 1 << statusFFCStateShift}, false},
		{telemetryRowA{StatusBits: 2 << statusFFCStateShift}, true},
		{telemetryRowA{StatusBits: 3 << statusFFCStateShift}, true},
	}
	for _, line := range data {
		buf := bytes.Buffer{}
		if err := binary.Write(&buf, internal.Big16, &line.rowA); err != nil {
			t.Fatal(err)
		}
		err := m.parseTelemetry(buf.Bytes())
		if line.success {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if err == nil {
				t.Fatal("expected failure")
			}
		}
	}
}

//

func initSequence() []i2ctest.IO {
	return []i2ctest.IO{
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 6, 0, 4}},                              // GetStatus()
		{Addr: 42, W: []byte{0, 4, 2, 4}},                              //
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 8}, R: []byte{0, 0, 0, 0, 0, 0, 0, 0}}, // GetStatus() result
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 8, 0, 0, 0, 0}},                        // Init()
		{Addr: 42, W: []byte{0, 6, 0, 0x2}},                            //
		{Addr: 42, W: []byte{0, 4, 1, 0x1}},                            //
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 8, 0, 1, 0, 0}},                        //
		{Addr: 42, W: []byte{0, 6, 0, 2}},                              //
		{Addr: 42, W: []byte{0, 4, 2, 0x19}},                           //
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
		{Addr: 42, W: []byte{0, 8, 0, 0, 0, 0}},                        //
		{Addr: 42, W: []byte{0, 6, 0, 2}},                              //
		{Addr: 42, W: []byte{0, 4, 2, 0x1d}},                           // Init() end
		{Addr: 42, W: []byte{0, 2}, R: []byte{0, 6}},                   // waitIdle
	}
}

func telemetryLine(t *testing.T) []byte {
	b := bytes.Buffer{}
	rowA := telemetryRowA{
		TelemetryRevision: 8,
		StatusBits:        statusFFCDesired,
		HousingTemp:       internal.CentiK(27515), // 2°C
	}
	if err := binary.Write(&b, internal.Big16, &rowA); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func appendHeader(t *testing.T, i int, d []byte) []byte {
	if len(d) != 160 {
		t.Fatalf("currently hardcoded for 80x60: %d", len(d))
	}
	out := make([]byte, 164)
	internal.Big16.PutUint16(out, uint16(i))
	copy(out[4:], d)
	calcCRC(out)
	return out
}

func referenceFrame() *image14bit.Gray14 {
	r := image.Rect(0, 0, 80, 60)
	img := image14bit.NewGray14(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetIntensity14(x, y, image14bit.Intensity14(uint16(8192-80+(x*2))))
		}
	}
	return img
}

func prepareFrame(t *testing.T) []byte {
	buf := bytes.Buffer{}
	tmp := make([]byte, 160)
	buf.Write(appendHeader(t, 0, telemetryLine(t)))
	buf.Write(appendHeader(t, 1, tmp))
	buf.Write(appendHeader(t, 2, tmp))
	img := referenceFrame()
	r := img.Bounds()
	for y := 0; y < r.Max.Y; y++ {
		for x := 0; x < r.Max.X; x++ {
			internal.Big16.PutUint16(tmp[x*2:], uint16(img.Intensity14At(x, y)))
		}
		buf.Write(appendHeader(t, y+3, tmp))
	}
	return buf.Bytes()
}

func calcCRC(d []byte) {
	tmp := make([]byte, len(d))
	copy(tmp, d)
	tmp[0] &^= 0x0F
	tmp[2] = 0
	tmp[3] = 0
	internal.Big16.PutUint16(d[2:], internal.CRC16(tmp))
}

type spiStream struct {
	t      *testing.T
	data   []byte
	offset int
	err    error
}

func (s *spiStream) String() string {
	return "spi"
}

func (s *spiStream) Connect(f physic.Frequency, mode spi.Mode, bits int) (spi.Conn, error) {
	if f != 20*physic.MegaHertz {
		s.t.Fatal(f)
	}
	if mode != spi.Mode3 {
		s.t.Fatal(mode)
	}
	if bits != 8 {
		s.t.Fatal(bits)
	}
	return s, s.err
}

func (s *spiStream) Tx(w, r []byte) error {
	if w != nil {
		s.t.Fatal("write is not implemented")
	}
	if s.offset < len(s.data) {
		copy(r, s.data[s.offset:])
		s.offset += len(r)
	}
	return s.err
}

func (s *spiStream) TxPackets(p []spi.Packet) error {
	s.t.Fatal("TxPackets is not implemented")
	return nil
}

func (s *spiStream) Duplex() conn.Duplex {
	return conn.DuplexUnknown
}

func (s *spiStream) MaxTxSize() int {
	return 7 * 164
}
