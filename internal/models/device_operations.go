package models

import (
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

// DeviceOperationsFields is operator-entered reference data, never execution policy.
type DeviceOperationsFields struct {
	Purpose             string  `json:"purpose"`
	Applications        string  `json:"applications"`
	Dependencies        string  `json:"dependencies"`
	Criticality         string  `json:"criticality"`
	MaintenanceWindow   string  `json:"maintenance_window"`
	MaintenanceTimezone string  `json:"maintenance_timezone"`
	BackupNotes         string  `json:"backup_notes"`
	BackupVerifiedOn    *string `json:"backup_verified_on"`
}

type DeviceOperationsProfile struct {
	DeviceOperationsFields
	Version   int64      `json:"version"`
	UpdatedAt *time.Time `json:"updated_at"`
	UpdatedBy string     `json:"updated_by"`
}

func DefaultDeviceOperationsProfile() DeviceOperationsProfile {
	return DeviceOperationsProfile{DeviceOperationsFields: DeviceOperationsFields{Criticality: "unknown"}}
}

func (p DeviceOperationsFields) Validate() error {
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"purpose", p.Purpose, 2000}, {"applications", p.Applications, 8000},
		{"dependencies", p.Dependencies, 8000}, {"backup_notes", p.BackupNotes, 8000},
		{"maintenance_window", p.MaintenanceWindow, 1000}, {"maintenance_timezone", p.MaintenanceTimezone, 100},
	} {
		if !utf8.ValidString(f.value) || utf8.RuneCountInString(f.value) > f.max {
			return fmt.Errorf("%s exceeds its character limit or is invalid UTF-8", f.name)
		}
		for _, c := range f.value {
			if (unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t') || unicode.Is(unicode.Cf, c) {
				return fmt.Errorf("%s contains unsupported control characters", f.name)
			}
		}
	}
	switch p.Criticality {
	case "unknown", "low", "normal", "high", "critical":
	default:
		return fmt.Errorf("invalid criticality")
	}
	if p.MaintenanceTimezone != "" {
		if p.MaintenanceTimezone == "Local" {
			return fmt.Errorf("maintenance_timezone must be an IANA timezone")
		}
		if _, err := time.LoadLocation(p.MaintenanceTimezone); err != nil {
			return fmt.Errorf("maintenance_timezone must be an IANA timezone")
		}
	}
	if p.BackupVerifiedOn != nil {
		date := *p.BackupVerifiedOn
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || len(date) != 10 || parsed.Year() < 1 || parsed.Format("2006-01-02") != date {
			return fmt.Errorf("backup_verified_on must be an ISO calendar date")
		}
	}
	return nil
}
