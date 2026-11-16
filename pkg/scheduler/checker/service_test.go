package checker_test

import (
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
)

func TestBuildtimeServiceAddsBdeps(t *testing.T) {
	tests := []struct {
		name    string
		service string
		want    []string
	}{
		{"buildtime", `<services><service name="tar" mode="buildtime"/></services>`, []string{"obs-service-tar"}},
		{"localonly", `<services><service name="tar" mode="localonly"/></services>`, nil},
		{"no service file", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t, []bininfo.BinInfo{binary("obs-service-tar", "0")})
			files := map[string]string{"p.spec": spec("p", nil)}
			if tt.service != "" {
				files["_service"] = tt.service
			}
			w.addPackage("p", files)
			w.run()
			var got []string
			for _, b := range w.wantJobs("p")[0].Bdeps {
				got = append(got, b.Name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bdeps %v, want %v", got, tt.want)
			}
		})
	}
}
