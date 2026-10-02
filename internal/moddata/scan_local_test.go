package moddata

import (
	"encoding/json"
	"os"
	"testing"
)

// Local harness: SCAN_DIR=<server dir> SCAN_DATA=<cache dir> SCAN_PARAMS=<json file> SCAN_OUT=<json file>
func TestScanLocalPack(t *testing.T) {
	dir := os.Getenv("SCAN_DIR")
	if dir == "" {
		t.Skip("SCAN_DIR not set")
	}
	params := map[string]interface{}{"mc_version": os.Getenv("SCAN_MC"), "loader": os.Getenv("SCAN_LOADER")}
	if p := os.Getenv("SCAN_PARAMS"); p != "" {
		data, _ := os.ReadFile(p)
		_ = json.Unmarshal(data, &params)
	}
	res := HandleModScan(dir, "local-test", os.Getenv("SCAN_DATA"), params)
	out, _ := json.Marshal(res)
	os.WriteFile(os.Getenv("SCAN_OUT"), out, 0644)
}
