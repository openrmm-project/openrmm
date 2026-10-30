package protocol

import (
	"strings"
	"testing"
)

func TestNormalizeDeviceQueryFillsDefaultsAndRefusesTheRest(t *testing.T) {
	got, err := NormalizeDeviceQuery(DeviceQueryRequest{Query: DeviceQueryProcesses, Params: map[string]string{"limit": " 007 "}})
	if err != nil || got.Params["limit"] != "7" || got.Params["sort"] != "cpu" {
		t.Fatalf("got %+v %v", got, err)
	}
	if got, err := NormalizeDeviceQuery(DeviceQueryRequest{Query: DeviceQueryDisk}); err != nil || got.Params != nil {
		t.Fatalf("disk: %+v %v", got, err)
	}
	for name, req := range map[string]DeviceQueryRequest{
		"unknown query":     {Query: "files"},
		"unknown parameter": {Query: DeviceQueryDisk, Params: map[string]string{"path": "/"}},
		"out of range":      {Query: DeviceQueryEvents, Params: map[string]string{"hours": "73"}},
		"not a number":      {Query: DeviceQueryProcesses, Params: map[string]string{"limit": "5; rm"}},
		"not in the list":   {Query: DeviceQueryNetwork, Params: map[string]string{"section": "routes"}},
		"shell in a filter": {Query: DeviceQueryServices, Params: map[string]string{"name": "x$(id)"}},
		"long filter":       {Query: DeviceQuerySoftware, Params: map[string]string{"name": strings.Repeat("a", 65)}},
	} {
		if _, err := NormalizeDeviceQuery(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEveryCatalogQueryNormalizesWithDefaults(t *testing.T) {
	for _, name := range DeviceQueryNames() {
		if _, err := NormalizeDeviceQuery(DeviceQueryRequest{Query: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(DeviceQueryNames()) != 7 {
		t.Fatalf("catalog = %v", DeviceQueryNames())
	}
}
