package monitor

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// insertBatchSize mimics the async writer, which commits batched transactions.
const insertBatchSize = 50

// insertDNSRows inserts rows in committed batches and returns how many rows
// were actually committed (a batch that hits the hard page limit is rolled back
// wholesale, like the real writer's flushBatch).
func insertDNSRows(t *testing.T, d *Database, from, to, payload int) int {
	t.Helper()
	answers := `["` + strings.Repeat("a", payload) + `"]`
	inserted := 0
	for start := from; start < to; start += insertBatchSize {
		end := start + insertBatchSize
		if end > to {
			end = to
		}
		committed, ok := insertDNSBatch(d, start, end, answers)
		inserted += committed
		if !ok {
			break
		}
	}
	return inserted
}

func insertDNSBatch(d *Database, from, to int, answers string) (int, bool) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, false
	}
	statement, err := tx.Prepare("INSERT INTO dns_records (timestamp_ms, domain, qtype, answers) VALUES (?,?,?,?)")
	if err != nil {
		tx.Rollback()
		return 0, false
	}
	for i := from; i < to; i++ {
		if _, err = statement.Exec(int64(i), fmt.Sprintf("host-%d.example.com", i), "A", answers); err != nil {
			statement.Close()
			tx.Rollback()
			return 0, false
		}
	}
	statement.Close()
	if err = tx.Commit(); err != nil {
		return 0, false
	}
	return to - from, true
}

func insertConnRows(t *testing.T, d *Database, from, to int) {
	t.Helper()
	tx, err := d.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare("INSERT INTO connection_records (conn_id, dest_ip, start_time_ms) VALUES (?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := from; i < to; i++ {
		if _, err = statement.Exec(fmt.Sprintf("conn-%d", i), "10.0.0.1", int64(i)); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatalf("insert conn row: %v", err)
		}
	}
	statement.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, d *Database, table string) int {
	t.Helper()
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mainDBSize(t *testing.T, d *Database) int64 {
	t.Helper()
	var pageCount, pageSize int64
	if err := d.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if err := d.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	return pageCount * pageSize
}

// TestRowCaps verifies the row caps keep only the newest records.
func TestRowCaps(t *testing.T) {
	db, err := NewDatabaseWithLimits(filepath.Join(t.TempDir(), "monitor.db"), Limits{
		MaxSizeBytes:  DefaultMaxSizeBytes,
		MaxDNSRows:    200,
		MaxConnRows:   100,
		RetentionDays: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if inserted := insertDNSRows(t, db, 0, 1000, 16); inserted != 1000 {
		t.Fatalf("expected 1000 dns rows inserted, got %d", inserted)
	}
	insertConnRows(t, db, 0, 500)
	if deleted := db.enforceLimits(); deleted == 0 {
		t.Fatal("expected rows to be pruned")
	}
	if dnsRows := countRows(t, db, "dns_records"); dnsRows != 200 {
		t.Fatalf("expected 200 dns rows, got %d", dnsRows)
	}
	if connRows := countRows(t, db, "connection_records"); connRows != 100 {
		t.Fatalf("expected 100 connection rows, got %d", connRows)
	}
	// The newest rows must be the survivors.
	var newest int64
	if err = db.db.QueryRow("SELECT MAX(timestamp_ms) FROM dns_records").Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if newest != 999 {
		t.Fatalf("expected the newest dns row (999) to survive, got %d", newest)
	}
}

// TestSizeCap verifies the database cannot grow past the configured size and
// that maintenance frees space again so writes recover (no hard wall).
func TestSizeCap(t *testing.T) {
	const maxSize = 1 << 20 // 1 MB
	db, err := NewDatabaseWithLimits(filepath.Join(t.TempDir(), "monitor.db"), Limits{
		MaxSizeBytes:        maxSize,
		MaxDNSRows:          -1,
		MaxConnRows:         -1,
		RetentionDays:       -1,
		MaintenanceInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Fill the database until the hard page limit refuses further batches.
	filled := insertDNSRows(t, db, 0, 2000, 500)
	if filled == 0 {
		t.Fatal("no rows could be inserted at all")
	}
	if filled >= 2000 {
		t.Fatal("the hard page limit never kicked in — expected the fill to be cut short")
	}
	hardLimit := db.hardLimitBytes()
	fillSize := mainDBSize(t, db)
	if fillSize < hardLimit*8/10 {
		t.Fatalf("expected the fill to reach the hard limit region, only got %d of %d", fillSize, hardLimit)
	}
	if fillSize > hardLimit {
		t.Fatalf("main file grew past its hard limit: %d > %d", fillSize, hardLimit)
	}
	if footprint := db.footprint(); footprint > maxSize {
		t.Fatalf("on-disk footprint exceeded the cap: %d > %d", footprint, maxSize)
	}
	t.Logf("hard page limit stopped the fill after %d rows (main %d of %d bytes, cap %d)",
		filled, fillSize, hardLimit, maxSize)

	// Maintenance must prune automatically once the footprint crosses the
	// pruning target, freeing space again.
	deadline := time.Now().Add(15 * time.Second)
	var rows int
	for time.Now().Before(deadline) {
		if rows = countRows(t, db, "dns_records"); rows < filled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rows >= filled {
		t.Fatalf("maintenance never pruned an over-cap database (still %d rows)", rows)
	}
	t.Logf("maintenance pruned %d → %d rows (main %d bytes, footprint %d bytes)",
		filled, rows, mainDBSize(t, db), db.footprint())

	// Writes must work again after the prune — the cap is not a hard wall.
	if inserted := insertDNSRows(t, db, 100000, 100050, 100); inserted == 0 {
		t.Fatal("writes never recovered after pruning — the cap is a hard wall")
	}
	if size := mainDBSize(t, db); size > hardLimit {
		t.Fatalf("main file grew past its hard limit after pruning: %d > %d", size, hardLimit)
	}
	if footprint := db.footprint(); footprint > maxSize {
		t.Fatalf("on-disk footprint exceeded the cap after pruning: %d > %d", footprint, maxSize)
	}
	if rows := countRows(t, db, "dns_records"); rows == 0 {
		t.Fatal("pruning deleted every record")
	}
}

// TestLimitsFrom pins the config mapping: 0 keeps the default, a negative value
// disables the cap, a positive value overrides it.
func TestLimitsFrom(t *testing.T) {
	defaults := DefaultLimits()
	unchanged := LimitsFrom(0, 0, 0, 0)
	if unchanged != defaults {
		t.Fatalf("zero values must keep the defaults: %+v", unchanged)
	}
	disabled := LimitsFrom(-1, -1, -1, -1)
	if disabled.MaxSizeBytes > 0 || disabled.MaxDNSRows > 0 || disabled.MaxConnRows > 0 || disabled.RetentionDays > 0 {
		t.Fatalf("negative values must disable the caps: %+v", disabled)
	}
	custom := LimitsFrom(50, 1000, 2000, 3)
	if custom.MaxSizeBytes != 50<<20 || custom.MaxDNSRows != 1000 || custom.MaxConnRows != 2000 || custom.RetentionDays != 3 {
		t.Fatalf("unexpected custom limits: %+v", custom)
	}
	// withDefaults must not resurrect a disabled cap.
	applied := disabled.withDefaults()
	if applied.MaxSizeBytes > 0 || applied.MaxDNSRows > 0 {
		t.Fatalf("withDefaults re-enabled a disabled cap: %+v", applied)
	}
}
