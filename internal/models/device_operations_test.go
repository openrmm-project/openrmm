package models

import (
	"strings"
	"testing"
)

func TestDeviceOperationsValidation(t *testing.T) {
	good := DefaultDeviceOperationsProfile().DeviceOperationsFields
	date := "2024-02-29"
	good.BackupVerifiedOn = &date
	good.MaintenanceTimezone = "Europe/Zurich"
	good.Purpose = strings.Repeat("😀", 2000)
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*DeviceOperationsFields)
	}{
		{"purpose limit", func(p *DeviceOperationsFields) { p.Purpose += "a" }},
		{"applications limit", func(p *DeviceOperationsFields) { p.Applications = strings.Repeat("a", 8001) }},
		{"dependencies limit", func(p *DeviceOperationsFields) { p.Dependencies = strings.Repeat("a", 8001) }},
		{"backup limit", func(p *DeviceOperationsFields) { p.BackupNotes = strings.Repeat("a", 8001) }},
		{"window limit", func(p *DeviceOperationsFields) { p.MaintenanceWindow = strings.Repeat("a", 1001) }},
		{"control", func(p *DeviceOperationsFields) { p.Purpose = "abc\x00" }},
		{"bidi", func(p *DeviceOperationsFields) { p.Purpose = "abc\u202e" }},
		{"enum", func(p *DeviceOperationsFields) { p.Criticality = "approved" }},
		{"timezone", func(p *DeviceOperationsFields) { p.MaintenanceTimezone = "Not/AZone" }},
		{"local timezone", func(p *DeviceOperationsFields) { p.MaintenanceTimezone = "Local" }},
		{"invalid day", func(p *DeviceOperationsFields) { v := "2026-02-29"; p.BackupVerifiedOn = &v }},
		{"datetime", func(p *DeviceOperationsFields) { v := "2026-01-01T00:00:00Z"; p.BackupVerifiedOn = &v }},
		{"zero year", func(p *DeviceOperationsFields) { v := "0000-01-01"; p.BackupVerifiedOn = &v }},
		{"invalid utf8", func(p *DeviceOperationsFields) { p.Purpose = string([]byte{0xff}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := good
			test.mutate(&p)
			if p.Validate() == nil {
				t.Fatal("accepted invalid fields")
			}
		})
	}
}
