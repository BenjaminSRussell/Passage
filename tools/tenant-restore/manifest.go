package main

import (
	"fmt"
	"io"
	"regexp"
	"text/template"
	"time"
)

var (
	dnsLabel    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	imagePrefix = regexp.MustCompile(`^[a-zA-Z0-9][-a-zA-Z0-9_./:@]*$`)
	storageSize = regexp.MustCompile(`^[1-9][0-9]*(Gi|Ti)$`)
)

type ScratchSpec struct {
	Name       string
	Namespace  string
	SourceName string
	StoreName  string
	TargetTime time.Time
	Image      string
	Storage    string
}

var scratchTemplate = template.Must(template.New("scratch").Parse(`apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/part-of: passage
    passage.dev/purpose: tenant-restore-scratch
spec:
  instances: 1
  imageName: {{ .Image }}
  enableSuperuserAccess: true
  storage:
    size: {{ .Storage }}
  bootstrap:
    recovery:
      source: origin
      database: passage
      owner: passage_owner
      recoveryTarget:
        targetTime: "{{ .TargetTimeText }}"
  externalClusters:
    - name: origin
      plugin:
        name: barman-cloud.cloudnative-pg.io
        parameters:
          barmanObjectName: {{ .StoreName }}
          serverName: {{ .SourceName }}
`))

func (s ScratchSpec) TargetTimeText() string {
	return s.TargetTime.UTC().Format("2006-01-02 15:04:05.999999-07")
}

func RenderScratchCluster(w io.Writer, s ScratchSpec, now time.Time) error {
	for field, value := range map[string]string{"name": s.Name, "namespace": s.Namespace, "source cluster": s.SourceName, "object store": s.StoreName} {
		if !dnsLabel.MatchString(value) {
			return fmt.Errorf("%s %q is not a Kubernetes name", field, value)
		}
	}
	if s.Name == s.SourceName {
		return fmt.Errorf("scratch cluster must not share the name of the source cluster %q", s.SourceName)
	}
	if !imagePrefix.MatchString(s.Image) {
		return fmt.Errorf("image %q is not an image reference", s.Image)
	}
	if !storageSize.MatchString(s.Storage) {
		return fmt.Errorf("storage %q must look like 500Gi", s.Storage)
	}
	if !s.TargetTime.Before(now) {
		return fmt.Errorf("target time %s is not in the past", s.TargetTime.Format(time.RFC3339))
	}
	return scratchTemplate.Execute(w, s)
}
