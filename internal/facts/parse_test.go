package facts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseRealUbuntuOutput runs against output captured from a real Ubuntu
// 24.04 system by executing CollectScript verbatim. Regenerate it with:
//
//	hlab run <host> -- "$(the contents of CollectScript)"
func TestParseRealUbuntuOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ubuntu-wsl.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	f := Parse("office", string(raw))

	if f.Partial {
		t.Error("Partial = true, want false: the fixture ends with the end marker")
	}
	if f.Hostname != "Office" {
		t.Errorf("Hostname = %q, want Office", f.Hostname)
	}
	if f.OS.Name != "Ubuntu" {
		t.Errorf("OS.Name = %q, want Ubuntu", f.OS.Name)
	}
	if f.OS.ID != "ubuntu" {
		t.Errorf("OS.ID = %q, want ubuntu", f.OS.ID)
	}
	if got := f.OS.Short(); got != "Ubuntu 24.04.4 LTS" {
		t.Errorf("OS.Short() = %q, want %q", got, "Ubuntu 24.04.4 LTS")
	}
	if f.Kernel != "6.6.87.2-microsoft-standard-WSL2" {
		t.Errorf("Kernel = %q", f.Kernel)
	}
	if f.Arch != "x86_64" {
		t.Errorf("Arch = %q, want x86_64", f.Arch)
	}
	if f.Init != "systemd" {
		t.Errorf("Init = %q, want systemd", f.Init)
	}
	if f.CPUs != 16 {
		t.Errorf("CPUs = %d, want 16", f.CPUs)
	}
	// Exact uptime depends on when the fixture was captured; exact parsing is
	// pinned in TestParseDebianWithProcessNames instead.
	if f.UptimeSeconds <= 0 {
		t.Errorf("UptimeSeconds = %d, want a positive value", f.UptimeSeconds)
	}
	if f.Memory.TotalKB != 16279732 {
		t.Errorf("Memory.TotalKB = %d", f.Memory.TotalKB)
	}
	if len(f.LoadAverage) != 3 {
		t.Errorf("LoadAverage = %v, want 3 values", f.LoadAverage)
	}

	// The fixture's df output is mostly WSL pseudo mounts and snap loopbacks;
	// only the real ext4 root should survive.
	if len(f.Disks) != 1 {
		t.Fatalf("Disks = %d, want 1 (only the ext4 root):\n%+v", len(f.Disks), f.Disks)
	}
	root, ok := f.RootDisk()
	if !ok {
		t.Fatal("root filesystem missing")
	}
	if root.Type != "ext4" || root.UsePercent != 2 {
		t.Errorf("root = %+v, want ext4 at 2%%", root)
	}

	if len(f.Services) != 16 {
		t.Errorf("Services = %d, want 16", len(f.Services))
	}
	var apache *Service
	for i := range f.Services {
		if f.Services[i].Name == "apache2.service" {
			apache = &f.Services[i]
		}
	}
	if apache == nil {
		t.Fatal("apache2.service not found")
	}
	if apache.Description != "The Apache HTTP Server" {
		t.Errorf("apache description = %q", apache.Description)
	}
	if apache.ShortName() != "apache2" {
		t.Errorf("apache ShortName = %q", apache.ShortName())
	}

	// Four listening sockets in the fixture: three on port 53 with distinct
	// bind addresses, plus *:80.
	ports := map[int]Port{}
	for _, p := range f.Ports {
		ports[p.Port] = p
	}
	if _, ok := ports[80]; !ok {
		t.Errorf("port 80 missing from %+v", f.Ports)
	}
	if p := ports[80]; p.Address != "*" || !p.Exposed() {
		t.Errorf("port 80 = %+v, want exposed wildcard", p)
	}
	if _, ok := ports[53]; !ok {
		t.Errorf("port 53 missing from %+v", f.Ports)
	}

	// The docker shim on this system prints an error to stdout instead of a
	// container list; none of it may be mistaken for a container.
	if len(f.Containers) != 0 {
		t.Errorf("Containers = %+v, want none", f.Containers)
	}
}

func TestParseDebianWithProcessNames(t *testing.T) {
	// ss run as root includes users:(("name",...)), which is where process
	// attribution comes from.
	raw := strings.Join([]string{
		"##HLAB#hostname#", "nas-01",
		"##HLAB#os-release#",
		`PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"`,
		`NAME="Debian GNU/Linux"`,
		`VERSION_ID="12"`,
		`VERSION="12 (bookworm)"`,
		"ID=debian",
		"##HLAB#kernel#", "6.1.0-18-amd64",
		"##HLAB#arch#", "aarch64",
		"##HLAB#uptime#", "1209600.42 4800000.10",
		"##HLAB#cpu#", "4",
		"##HLAB#memory#", "MemTotal:        8000000 kB", "MemAvailable:     2000000 kB",
		"##HLAB#load#", "0.52 0.31 0.20 1/300 999",
		"##HLAB#disk#",
		"Filesystem     Type     1024-blocks      Used Available Capacity Mounted on",
		"/dev/sda1      ext4        61255492   5825852  52285524      11% /",
		"tank           zfs       9000000000 100000000 8900000000       2% /tank",
		"tmpfs          tmpfs         819200         0    819200       0% /run",
		"##HLAB#init#", "systemd",
		"##HLAB#services#",
		"smbd.service     loaded active running Samba SMB Daemon",
		"ssh.service      loaded active running OpenBSD Secure Shell server",
		"zfs-zed.service  loaded active running ZFS Event Daemon",
		"##HLAB#ports#",
		`LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=800,fd=3))`,
		`LISTEN 0 128 [::]:22 [::]:* users:(("sshd",pid=800,fd=4))`,
		`LISTEN 0 50 0.0.0.0:445 0.0.0.0:* users:(("smbd",pid=1200,fd=35))`,
		`LISTEN 0 128 127.0.0.1:5432 0.0.0.0:* users:(("postgres",pid=1500,fd=5))`,
		"##HLAB#containers#",
		"docker|caddy|caddy:2|Up 3 days",
		"docker|grafana|grafana/grafana:11|Up 2 weeks",
		"##HLAB#end#",
	}, "\n")

	f := Parse("nas-01", raw)

	if f.OS.Short() != "Debian GNU/Linux 12" {
		t.Errorf("OS.Short() = %q, want %q", f.OS.Short(), "Debian GNU/Linux 12")
	}
	if f.Uptime() != "14d 0h" {
		t.Errorf("Uptime() = %q, want 14d 0h", f.Uptime())
	}
	if got := f.Memory.UsedPercent(); got != 75 {
		t.Errorf("Memory.UsedPercent() = %d, want 75", got)
	}

	// tmpfs is dropped; ext4 and zfs are kept.
	if len(f.Disks) != 2 {
		t.Errorf("Disks = %d, want 2: %+v", len(f.Disks), f.Disks)
	}

	// sshd binds both v4 and v6 on 22: that is one service, not two.
	if len(f.Ports) != 3 {
		t.Fatalf("Ports = %d, want 3 after collapsing dual-stack: %+v", len(f.Ports), f.Ports)
	}
	byPort := map[int]Port{}
	for _, p := range f.Ports {
		byPort[p.Port] = p
	}
	if p := byPort[22]; p.Process != "sshd" || p.Address != "*" {
		t.Errorf("port 22 = %+v, want sshd on *", p)
	}
	if p := byPort[5432]; p.Address != "127.0.0.1" || p.Exposed() {
		t.Errorf("port 5432 = %+v, want loopback-only", p)
	}

	if len(f.Containers) != 2 {
		t.Fatalf("Containers = %d, want 2", len(f.Containers))
	}
	if f.Containers[0].Name != "caddy" || f.Containers[0].Runtime != "docker" {
		t.Errorf("Containers[0] = %+v", f.Containers[0])
	}
}

func TestParseNetstatFallback(t *testing.T) {
	raw := strings.Join([]string{
		"##HLAB#ports#",
		"Active Internet connections (only servers)",
		"Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name",
		"tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN      800/sshd",
		"tcp        0      0 127.0.0.1:3306          0.0.0.0:*               LISTEN      1400/mysqld",
		"tcp6       0      0 :::80                   :::*                    LISTEN      900/nginx",
		"tcp        0      0 198.51.100.5:44321          198.51.100.9:22             ESTABLISHED 1234/ssh",
		"##HLAB#end#",
	}, "\n")

	f := Parse("box", raw)

	if len(f.Ports) != 3 {
		t.Fatalf("Ports = %d, want 3 (established connections excluded): %+v", len(f.Ports), f.Ports)
	}

	byPort := map[int]Port{}
	for _, p := range f.Ports {
		byPort[p.Port] = p
	}
	if p := byPort[22]; p.Process != "sshd" {
		t.Errorf("port 22 = %+v, want sshd", p)
	}
	if p := byPort[3306]; p.Address != "127.0.0.1" || p.Process != "mysqld" {
		t.Errorf("port 3306 = %+v, want mysqld on loopback", p)
	}
	if p := byPort[80]; p.Address != "*" || p.Process != "nginx" {
		t.Errorf("port 80 = %+v, want nginx on *", p)
	}
}

func TestParseOpenRC(t *testing.T) {
	raw := strings.Join([]string{
		"##HLAB#os-release#", `NAME="Alpine Linux"`, "ID=alpine", `VERSION_ID="3.20.3"`,
		"##HLAB#init#", "openrc",
		"##HLAB#services#",
		" sshd                       [  started  ]",
		" crond                      [  started  ]",
		" chronyd                    [  stopped  ]",
		"##HLAB#end#",
	}, "\n")

	f := Parse("alpine-01", raw)

	if f.Init != "openrc" {
		t.Errorf("Init = %q, want openrc", f.Init)
	}
	if f.OS.Short() != "Alpine Linux 3.20.3" {
		t.Errorf("OS.Short() = %q", f.OS.Short())
	}
	if len(f.Services) != 2 {
		t.Fatalf("Services = %d, want 2 (stopped excluded): %+v", len(f.Services), f.Services)
	}
	names := f.ServiceNames()
	if strings.Join(names, ",") != "crond,sshd" {
		t.Errorf("ServiceNames = %v, want [crond sshd]", names)
	}
}

func TestParseTruncatedOutputIsFlaggedPartial(t *testing.T) {
	// No end marker: the connection dropped mid-collection.
	raw := "##HLAB#hostname#\nbox\n##HLAB#kernel#\n6.1.0\n"

	f := Parse("box", raw)

	if !f.Partial {
		t.Error("Partial = false, want true when the end marker is missing")
	}
	if f.Hostname != "box" {
		t.Errorf("Hostname = %q, want box", f.Hostname)
	}
}

func TestParseIgnoresPreambleBeforeFirstSection(t *testing.T) {
	// Some hosts print a MOTD or banner before the command output.
	raw := "Welcome to Example Linux!\nLast login: Tue Jul 27\n##HLAB#hostname#\nbox\n##HLAB#end#\n"

	f := Parse("box", raw)

	if f.Hostname != "box" {
		t.Errorf("Hostname = %q, want box", f.Hostname)
	}
	if f.Partial {
		t.Error("Partial = true, want false")
	}
}

func TestParseEmptyAndMissingSections(t *testing.T) {
	// A minimal host where nearly every probe found nothing.
	raw := strings.Join([]string{
		"##HLAB#hostname#", "tiny",
		"##HLAB#os-release#",
		"##HLAB#kernel#", "5.15.0",
		"##HLAB#uptime#",
		"##HLAB#cpu#",
		"##HLAB#disk#",
		"##HLAB#services#",
		"##HLAB#ports#",
		"##HLAB#containers#",
		"##HLAB#end#",
	}, "\n")

	f := Parse("tiny", raw)

	if f.Partial {
		t.Error("Partial = true, want false: empty sections are not truncation")
	}
	if f.Hostname != "tiny" || f.Kernel != "5.15.0" {
		t.Errorf("Hostname = %q, Kernel = %q", f.Hostname, f.Kernel)
	}
	if f.CPUs != 0 || f.UptimeSeconds != 0 {
		t.Errorf("CPUs = %d, UptimeSeconds = %d, want zero", f.CPUs, f.UptimeSeconds)
	}
	if f.Uptime() != "" {
		t.Errorf("Uptime() = %q, want empty", f.Uptime())
	}
	if f.Memory.UsedPercent() != -1 {
		t.Errorf("UsedPercent = %d, want -1 when unknown", f.Memory.UsedPercent())
	}
	if len(f.Disks)+len(f.Services)+len(f.Ports)+len(f.Containers) != 0 {
		t.Error("expected all lists empty")
	}
}

func TestParseDiskWithSpacesInMountPoint(t *testing.T) {
	raw := strings.Join([]string{
		"##HLAB#disk#",
		"Filesystem     Type     1024-blocks      Used Available Capacity Mounted on",
		"/dev/sdb1      ext4         1000000    500000    500000      50% /mnt/my backup drive",
		"##HLAB#end#",
	}, "\n")

	f := Parse("box", raw)

	if len(f.Disks) != 1 {
		t.Fatalf("Disks = %d, want 1", len(f.Disks))
	}
	if f.Disks[0].MountPoint != "/mnt/my backup drive" {
		t.Errorf("MountPoint = %q", f.Disks[0].MountPoint)
	}
}

func TestParseCollapsingKeepsWidestBinding(t *testing.T) {
	// Without process names (an unprivileged login) several sockets on one port
	// collapse together. The merged entry must not understate reachability by
	// keeping whichever loopback address happened to be parsed first.
	raw := strings.Join([]string{
		"##HLAB#ports#",
		"LISTEN 0 4096 127.0.0.54:53 0.0.0.0:*",
		"LISTEN 0 1000 10.255.255.254:53 0.0.0.0:*",
		"LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*",
		"##HLAB#end#",
	}, "\n")

	f := Parse("box", raw)

	if len(f.Ports) != 1 {
		t.Fatalf("Ports = %d, want 1: %+v", len(f.Ports), f.Ports)
	}
	p := f.Ports[0]
	if p.Address != "10.255.255.254" {
		t.Errorf("Address = %q, want the externally reachable 10.255.255.254", p.Address)
	}
	if !p.Exposed() {
		t.Error("Exposed() = false, want true: it is bound to a real interface")
	}
	if p.Scope() != "10.255.255.254" {
		t.Errorf("Scope() = %q", p.Scope())
	}
}

func TestPortExposureAndScope(t *testing.T) {
	tests := []struct {
		address   string
		exposed   bool
		wantScope string
	}{
		{"*", true, "all interfaces"},
		{"127.0.0.1", false, "localhost"},
		{"127.0.0.53", false, "localhost"},
		{"::1", false, "localhost"},
		{"198.51.100.5", true, "198.51.100.5"},
		{"fe80::1", true, "fe80::1"},
	}

	for _, tc := range tests {
		t.Run(tc.address, func(t *testing.T) {
			p := Port{Address: tc.address, Port: 53}
			if p.Exposed() != tc.exposed {
				t.Errorf("Exposed() = %v, want %v", p.Exposed(), tc.exposed)
			}
			if p.Scope() != tc.wantScope {
				t.Errorf("Scope() = %q, want %q", p.Scope(), tc.wantScope)
			}
		})
	}
}

func TestParsePrivilegeLevel(t *testing.T) {
	for _, level := range []string{"root", "sudo", "none"} {
		f := Parse("box", "##HLAB#privileged#\n"+level+"\n##HLAB#end#\n")
		if f.Privilege != level {
			t.Errorf("Privilege = %q, want %q", f.Privilege, level)
		}
	}
}

func TestParseContainersDeduplicates(t *testing.T) {
	// The script asks docker both unprivileged and via sudo, since the
	// management account is usually not in the docker group. When both
	// succeed the same container is reported twice.
	raw := strings.Join([]string{
		"##HLAB#containers#",
		"docker|caddy|caddy:2|Up 3 days",
		"docker|grafana|grafana/grafana:11|Up 2 weeks",
		"docker|caddy|caddy:2|Up 3 days",
		"docker|grafana|grafana/grafana:11|Up 2 weeks",
		"##HLAB#end#",
	}, "\n")

	f := Parse("docker-01", raw)

	if len(f.Containers) != 2 {
		t.Fatalf("Containers = %d, want 2 after dedup: %+v", len(f.Containers), f.Containers)
	}
	if f.Containers[0].Name != "caddy" || f.Containers[1].Name != "grafana" {
		t.Errorf("Containers = %+v", f.Containers)
	}
}

func TestParseContainersKeepsSameNameAcrossRuntimes(t *testing.T) {
	// Deduping must be per runtime: docker and podman can both have a "web".
	raw := strings.Join([]string{
		"##HLAB#containers#",
		"docker|web|nginx:1|Up",
		"podman|web|nginx:1|Up",
		"##HLAB#end#",
	}, "\n")

	if got := Parse("box", raw).Containers; len(got) != 2 {
		t.Errorf("Containers = %+v, want both runtimes kept", got)
	}
}

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		in       string
		wantAddr string
		wantPort int
		wantOK   bool
	}{
		{"0.0.0.0:22", "*", 22, true},
		{"[::]:22", "*", 22, true},
		{"*:80", "*", 80, true},
		{":::80", "*", 80, true},
		{"127.0.0.1:5432", "127.0.0.1", 5432, true},
		{"127.0.0.53%lo:53", "127.0.0.53", 53, true},
		{"10.255.255.254:53", "10.255.255.254", 53, true},
		{"[fe80::1]:8080", "fe80::1", 8080, true},
		{"nonsense", "", 0, false},
		{"1.2.3.4:notaport", "", 0, false},
		{"1.2.3.4:99999", "", 0, false},
		{"1.2.3.4:0", "", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			addr, port, ok := splitHostPort(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if addr != tc.wantAddr || port != tc.wantPort {
				t.Errorf("= (%q, %d), want (%q, %d)", addr, port, tc.wantAddr, tc.wantPort)
			}
		})
	}
}

func TestSuggestedTags(t *testing.T) {
	f := Facts{
		OS:   OSInfo{ID: "debian"},
		Arch: "aarch64",
		Services: []Service{
			{Name: "smbd.service"},
			{Name: "zfs-zed.service"},
			{Name: "unattended-upgrades.service"},
		},
		Ports:      []Port{{Port: 22, Process: "sshd"}, {Port: 5432, Process: "postgresql"}},
		Containers: []Container{{Runtime: "docker", Name: "caddy"}},
	}

	got := strings.Join(f.SuggestedTags(), ",")
	// postgresql on 5432 contributes "database" via the port's process name.
	want := "aarch64,database,debian,docker,fileserver,ssh,zfs"

	if got != want {
		t.Errorf("SuggestedTags() = %q, want %q", got, want)
	}
}

func TestSuggestedTagsIgnoresUnknownServices(t *testing.T) {
	// A wrong tag is worse than no tag, so unrecognised services contribute
	// nothing.
	f := Facts{Services: []Service{{Name: "some-bespoke-thing.service"}}}

	if got := f.SuggestedTags(); len(got) != 0 {
		t.Errorf("SuggestedTags() = %v, want empty", got)
	}
}

func TestSectionNameRejectsMalformedMarkers(t *testing.T) {
	bad := []string{
		"##HLAB#",
		"##HLAB##",
		"##HLAB#two words#",
		"not a marker",
		"##HLAB#nohash",
	}
	for _, line := range bad {
		if name, ok := sectionName(line); ok {
			t.Errorf("sectionName(%q) = %q, want rejection", line, name)
		}
	}

	if name, ok := sectionName("##HLAB#os-release#"); !ok || name != "os-release" {
		t.Errorf("sectionName(valid) = (%q, %v)", name, ok)
	}
}
