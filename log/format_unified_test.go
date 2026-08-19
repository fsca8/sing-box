package log

import (
	"bytes"
	"testing"
)

func TestConvertLine(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		convert bool
	}{
		{
			"+0800 2026-08-18 16:19:58 INFO network: updated default interface WLAN, index 17",
			"2026-08-18T16:19:58.000+08:00 |I| engine|network|updated default interface WLAN, index 17",
			true,
		},
		{
			"+0800 2026-08-18 16:19:58 WARN inbound/tun[tun-in]: inbound DNS packet from 172.19.0.1",
			"2026-08-18T16:19:58.000+08:00 |W| engine|inbound/tun[tun-in]|inbound DNS packet from 172.19.0.1",
			true,
		},
		{
			"+0800 2026-08-18 16:19:58 ERROR outbound/vless[proxy]: dial tcp: connection refused",
			"2026-08-18T16:19:58.000+08:00 |E| engine|outbound/vless[proxy]|dial tcp: connection refused",
			true,
		},
		{
			"-0700 2026-08-18 01:19:58 DEBUG dialer: resolved 1.1.1.1",
			"2026-08-18T01:19:58.000-07:00 |D| engine|dialer|resolved 1.1.1.1",
			true,
		},
		{
			"+0800 2026-08-18 16:19:58 INFO sing-box started",
			"2026-08-18T16:19:58.000+08:00 |I| engine||sing-box started",
			true,
		},
		{
			"+0800 2026-08-18 16:19:58 FATAL read config: boom",
			"2026-08-18T16:19:58.000+08:00 |F| engine||read config: boom",
			true,
		},
		{
			"not a sing-box line at all",
			"",
			false,
		},
		{
			"2026-08-18T16:19:58.000+08:00 |I| app|ctrl|already unified",
			"",
			false, // 统一格式行不应再被当原生行转换（原样透传）
		},
	}
	for _, c := range cases {
		got, ok := convertLine(c.in)
		if ok != c.convert {
			t.Errorf("convertLine(%q) ok=%v want %v", c.in, ok, c.convert)
			continue
		}
		if ok && got != c.want {
			t.Errorf("convertLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnifiedWriterPassthrough(t *testing.T) {
	var buf bytes.Buffer
	u := &unifiedWriter{w: &buf}
	in := "+0800 2026-08-18 16:19:58 INFO network: up\n" +
		"raw line that cannot convert\n" +
		"+0800 2026-08-18 16:19:59 ERROR dialer: fail\n"
	if _, err := u.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "2026-08-18T16:19:58.000+08:00 |I| engine|network|up\n" +
		"raw line that cannot convert\n" +
		"2026-08-18T16:19:59.000+08:00 |E| engine|dialer|fail\n"
	if got != want {
		t.Errorf("unifiedWriter output mismatch:\n got: %q\nwant: %q", got, want)
	}
}
