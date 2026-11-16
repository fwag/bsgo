package recipe

import (
	"reflect"
	"testing"
)

func TestBuildtimeServices(t *testing.T) {
	tests := []struct {
		name, xml string
		want      []string
		wantErr   bool
	}{
		{"buildtime only", `<services>
<service name="tar" mode="buildtime"/>
<service name="recompress" mode="buildtime"><param name="compression">xz</param></service>
<service name="download_files" mode="localonly"/>
<service name="set_version"/>
<service name="x" mode="manual"/>
<service name="y" mode="disabled"/>
</services>`, []string{"tar", "recompress"}, false},
		{"none", `<services><service name="a" mode="trylocal"/></services>`, nil, false},
		{"empty", `<services/>`, nil, false},
		{"bad xml", `<services>`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildtimeServices([]byte(tt.xml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceDeps(t *testing.T) {
	sub := map[string][]string{"obs-service:tar": {"my-tar", "extra"}}
	tests := []struct {
		name  string
		names []string
		typ   string
		want  []string
	}{
		{"default", []string{"recompress", "set_version"}, "rpm", []string{"obs-service-recompress", "obs-service-set_version"}},
		{"deb underscore", []string{"set_version"}, "deb", []string{"obs-service-set-version"}},
		{"substitute", []string{"tar", "recompress"}, "rpm", []string{"my-tar", "extra", "obs-service-recompress"}},
		{"unique", []string{"a", "a"}, "rpm", []string{"obs-service-a"}},
		{"none", nil, "rpm", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceDeps(tt.names, sub, tt.typ); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
