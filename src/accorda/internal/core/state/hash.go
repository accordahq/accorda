package state

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// Hash returns the canonical SHA-256 hash of the service's normalized
// configuration (docs/ACCORDA.md §10). The hash is computed over a
// deterministic canonical representation of the reconciliation-relevant
// fields (image, command, env, env_file, ports, volumes, networks, labels,
// label_file, healthcheck, depends_on), so two services that differ only in
// the ordering of unordered
// collections (env, labels, ports, volumes, networks, depends_on) hash
// identically.
//
// The hash is used to decide whether a service requires recreation: when the
// desired service's hash differs from the deployed service's hash, the
// service's definition changed and it must be recreated.
func (s Service) Hash() string {
	sum := sha256.Sum256([]byte(s.canonical()))
	return hex.EncodeToString(sum[:])
}

// canonical returns a deterministic string encoding of the service's
// reconciliation-relevant fields. Unordered collections are sorted so
// reordering-equivalent configs produce identical output, honoring the
// determinism contract (docs/DECISIONS.md #7).
func (s Service) canonical() string {
	var b strings.Builder

	b.WriteString("image=")
	b.WriteString(s.Image)
	b.WriteByte('\n')

	b.WriteString("env_files=")
	writeExternalFiles(&b, s.EnvFiles)
	b.WriteByte('\n')

	b.WriteString("command=")
	for i, c := range s.Command {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(c)
	}
	b.WriteByte('\n')

	b.WriteString("env=")
	for _, k := range sortedStringKeys(s.Env) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Env[k])
		b.WriteByte(0)
	}
	b.WriteByte('\n')

	b.WriteString("ports=")
	for _, p := range sortedPorts(s.Ports) {
		b.WriteString(p.HostIP)
		b.WriteByte('|')
		b.WriteString(p.Host)
		b.WriteByte('|')
		b.WriteString(p.Container)
		b.WriteByte('|')
		b.WriteString(p.Protocol)
		b.WriteByte(0)
	}
	b.WriteByte('\n')

	b.WriteString("volumes=")
	for _, v := range sortedVolumes(s.Volumes) {
		b.WriteString(v.Type)
		b.WriteByte('|')
		b.WriteString(v.Source)
		b.WriteByte('|')
		b.WriteString(v.Target)
		b.WriteByte('|')
		b.WriteString(strconv.FormatBool(v.ReadOnly))
		b.WriteByte(0)
	}
	b.WriteByte('\n')

	b.WriteString("networks=")
	for i, n := range sortedStrings(s.Networks) {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(n)
	}
	b.WriteByte('\n')

	b.WriteString("labels=")
	for _, k := range sortedStringKeys(s.Labels) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Labels[k])
		b.WriteByte(0)
	}
	b.WriteByte('\n')

	b.WriteString("label_files=")
	writeExternalFiles(&b, s.LabelFiles)
	b.WriteByte('\n')

	b.WriteString("healthcheck=")
	b.WriteString(s.Healthcheck.canonical())
	b.WriteByte('\n')

	b.WriteString("depends_on=")
	for i, d := range sortedStrings(s.DependsOn) {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(d)
	}
	b.WriteByte('\n')

	b.WriteString("one_shot=")
	b.WriteString(strconv.FormatBool(s.OneShot))
	b.WriteByte('\n')

	return b.String()
}

// writeExternalFiles preserves declaration order because later env_file and
// label_file entries override earlier ones under Compose semantics.
func writeExternalFiles(b *strings.Builder, files []ExternalFile) {
	for _, file := range files {
		b.WriteString(file.Path)
		b.WriteByte('|')
		b.WriteString(strconv.FormatBool(file.Required))
		b.WriteByte('|')
		b.WriteString(file.Format)
		b.WriteByte('|')
		b.WriteString(file.Digest)
		b.WriteByte(0)
	}
}

// canonical returns a deterministic string encoding of the healthcheck. The
// Test command is ordered (exec form), so it is preserved verbatim; the
// scalar fields follow in a fixed order.
func (h Healthcheck) canonical() string {
	var b strings.Builder
	for i, t := range h.Test {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(t)
	}
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(int64(h.Interval), 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(int64(h.Timeout), 10))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(h.Retries))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(int64(h.StartPeriod), 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(h.Disable))
	return b.String()
}

// sortedStringKeys returns the keys of m in sorted order so map-derived
// output is deterministic (docs/DECISIONS.md #7).
func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedStrings returns a sorted copy of s so slice ordering does not affect
// the canonical representation.
func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// sortedPorts returns a copy of ports sorted by a canonical key so port
// ordering does not affect the canonical representation.
func sortedPorts(ports []Port) []Port {
	out := append([]Port(nil), ports...)
	sort.Slice(out, func(i, j int) bool {
		return portKey(out[i]) < portKey(out[j])
	})
	return out
}

// portKey returns a canonical sort key for a port.
func portKey(p Port) string {
	return p.HostIP + "|" + p.Host + "|" + p.Container + "|" + p.Protocol
}

// sortedVolumes returns a copy of vols sorted by a canonical key so volume
// ordering does not affect the canonical representation.
func sortedVolumes(vols []Volume) []Volume {
	out := append([]Volume(nil), vols...)
	sort.Slice(out, func(i, j int) bool {
		return volumeKey(out[i]) < volumeKey(out[j])
	})
	return out
}

// volumeKey returns a canonical sort key for a volume.
func volumeKey(v Volume) string {
	return v.Type + "|" + v.Source + "|" + v.Target + "|" + strconv.FormatBool(v.ReadOnly)
}
