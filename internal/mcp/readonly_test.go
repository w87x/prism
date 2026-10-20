package mcp

import "testing"

func TestLooksReadOnlyFromTheToolName(t *testing.T) {
	for name, want := range map[string]bool{
		"jackett_search": true, "jackett_list_indexers": true, "jackett_categories": true, "synology_ds_list": true, "synology_status": true,
		"getWeather": true, "list-files": true,
		"synology_ds_add": false, "synology_ds_pause": false, "synology_ds_delete": false, "synology_ds_clear_completed": false,
		"delete-all": false, "list_and_delete": false, "run_query": false, "echo": false, "download_status": false,
	} {
		if got := looksReadOnly(name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}
