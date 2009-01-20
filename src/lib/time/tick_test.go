// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package time

import (
	"testing";
	"time";
)

func TestTick(t *testing.T) {
	const (
		Delta = 100*1e6;
		Count = 10;
	);
	c := Tick(Delta);
	t0 := Nanoseconds();
	for i := 0; i < Count; i++ {
		<-c;
	}
	t1 := Nanoseconds();
	ns := t1 - t0;
	target := int64(Delta*Count);
	slop := target*2/10;
	if ns < target - slop || ns > target + slop {
		t.Fatalf("%d ticks of %g ns took %g ns, expected %g", Count, float64(Delta), float64(ns), float64(target));
	}
}
