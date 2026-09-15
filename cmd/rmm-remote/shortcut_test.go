package main

import (
	"reflect"
	"testing"
)

func TestShortcutReleasesKeysInReverseOrder(t *testing.T) {
	type event struct {
		code int
		down bool
	}
	var got []event
	shortcutKeys([]int{17, 18, 46}, func(code int, down bool) { got = append(got, event{code, down}) })
	want := []event{{17, true}, {18, true}, {46, true}, {46, false}, {18, false}, {17, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}
