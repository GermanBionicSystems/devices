// Copyright 2018 The Periph Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package as7262_test

import (
	"fmt"
	"log"
	"time"

	"periph.io/x/conn/v3/physic"

	"periph.io/x/conn/v3/i2c/i2creg"
	"github.com/GermanBionicSystems/devices/v3/as7262"
	"periph.io/x/host/v3"
)

func Example() {
	// Make sure periph is initialized.
	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	// Open default I²C bus.
	bus, err := i2creg.Open("")
	if err != nil {
		log.Fatalf("failed to open I²C: %v", err)
	}
	defer bus.Close()

	// Create a new spectrum sensor.
	sensor, err := as7262.New(bus, &as7262.DefaultOpts)
	if err != nil {
		log.Fatalln(err)
	}

	// Read values from sensor.
	spectrum, err := sensor.Sense(25*physic.MilliAmpere, 500*time.Millisecond)

	if err != nil {
		log.Fatalln(err)
	}

	fmt.Println(spectrum)
}
