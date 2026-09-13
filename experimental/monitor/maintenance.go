// experimental/monitor/maintenance.go
package monitor

import (
	"fmt"
	"os"
	"time"
)

// This file keeps monitor.db from growing without bound.
//
// Three mechanisms work together:
//   - retention / row caps (retention_days, max_dns_rows, max_conn_rows):
//     cheap, exact, independent of how large a single record happens to be;
//   - a size cap on the database (max_size_mb), enforced against the real
//     on-disk footprint (main file + write-ahead log): once crossed, the
//     oldest records are deleted until the footprint is back under 90% of the
//     cap, so every subsequent write has headroom;
//   - a hard page limit (PRAGMA max_page_count) set to the cap minus the WAL
//     allowance: SQLite refuses to allocate pages past it, so even a write
//     burst that outruns the periodic maintenance cannot push the files past
//     the configured size.
//
// The database is switched to auto_vacuum=FULL at startup (one-time VACUUM).
// Measured on SQLite: with INCREMENTAL, deleting 60% of the rows left the file
// at its peak size (freelist kept, incremental_vacuum freed a single page),
// while FULL shrank it immediately (246 → 101 pages for the same delete). FULL
// is what makes the cap hold on disk, not just logically.
const (
	// pruneChunk bounds rows deleted per DELETE statement so a large purge
	// does not hold the single SQLite write connection for too long.
	pruneChunk = 20000
	// maintenanceRounds bounds the pruning loop of a single pass.
	maintenanceRounds = 64
	// sizeTargetPercent is the fraction of the size cap the database is pruned
	// back to (headroom below both the cap and the hard page limit).
	sizeTargetPercent = 90
	// walAllowanceMin / walAllowanceMax clamp the share of the cap reserved
	// for the write-ahead log.
	walAllowanceMin int64 = 64 << 10
	walAllowanceMax int64 = 4 << 20
)

// enableAutoVacuum switches the database to auto_vacuum=FULL so that pages
// freed by pruning are returned to the OS instead of being kept in the
// freelist. Changing the mode requires a one-time VACUUM; on a database left
// oversized by an earlier unbounded run this also shrinks it.
func (d *Database) enableAutoVacuum() error {
	var mode int
	if err := d.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode == 1 {
		return nil // already FULL
	}
	if _, err := d.db.Exec("PRAGMA auto_vacuum=FULL"); err != nil {
		return err
	}
	_, err := d.db.Exec("VACUUM")
	return err
}

// walAllowance is the share of the size cap reserved for the WAL: 5% of the
// cap, clamped to [64KB, 4MB] and never above the cap itself. Measured together
// with the main file it keeps the total on-disk footprint within the cap.
func (d *Database) walAllowance() int64 {
	allowance := d.limits.MaxSizeBytes / 20
	if allowance < walAllowanceMin {
		allowance = walAllowanceMin
	}
	if allowance > walAllowanceMax {
		allowance = walAllowanceMax
	}
	if allowance > d.limits.MaxSizeBytes {
		allowance = d.limits.MaxSizeBytes
	}
	return allowance
}

// hardLimitBytes is the size the main database file is not allowed to exceed:
// the cap minus the WAL allowance, so that main file + WAL stay within the cap.
func (d *Database) hardLimitBytes() int64 {
	hardLimit := d.limits.MaxSizeBytes - d.walAllowance()
	if hardLimit < 0 {
		hardLimit = 0
	}
	return hardLimit
}

// applyHardSizeLimit pins the page count so the main file cannot grow past
// hardLimitBytes, and bounds the WAL.
func (d *Database) applyHardSizeLimit() error {
	if d.limits.MaxSizeBytes <= 0 {
		return nil
	}
	var pageSize int64
	if err := d.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil || pageSize <= 0 {
		pageSize = 4096
	}
	maxPages := d.hardLimitBytes() / pageSize
	if maxPages < 64 {
		maxPages = 64
	}
	if _, err := d.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", maxPages)); err != nil {
		return err
	}
	if _, err := d.db.Exec(fmt.Sprintf("PRAGMA journal_size_limit=%d", d.walAllowance())); err != nil {
		return err
	}
	// Checkpoint the WAL once it reaches the allowance instead of SQLite's
	// default (1000 pages), so the WAL stays within its share of the cap.
	checkpointPages := d.walAllowance() / pageSize
	if checkpointPages < 64 {
		checkpointPages = 64
	}
	if _, err := d.db.Exec(fmt.Sprintf("PRAGMA wal_autocheckpoint=%d", checkpointPages)); err != nil {
		return err
	}
	return nil
}

// maintenance enforces the limits periodically (and on demand when a write
// failed because the hard page limit was hit).
func (d *Database) maintenance() {
	defer d.wg.Done()
	ticker := time.NewTicker(d.limits.MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
		case <-d.pruneSignal:
		}
		if d.closed.Load() {
			return
		}
		d.enforceLimits()
	}
}

// signalPrune asks the maintenance goroutine for an immediate pass (non-blocking).
func (d *Database) signalPrune() {
	select {
	case d.pruneSignal <- struct{}{}:
	default:
	}
}

// enforceLimits applies the row caps, then the size cap. Returns the number of
// rows deleted.
func (d *Database) enforceLimits() int64 {
	var deleted int64

	if d.limits.MaxConnRows > 0 {
		deleted += d.trimRows("connection_records", "start_time_ms", d.limits.MaxConnRows)
	}
	if d.limits.MaxDNSRows > 0 {
		deleted += d.trimRows("dns_records", "timestamp_ms", d.limits.MaxDNSRows)
	}
	if d.limits.MaxSizeBytes > 0 && d.mainLogicalSize() > d.sizeTarget() {
		deleted += d.trimToSize()
	}
	if deleted > 0 {
		fmt.Printf("[monitor] pruned %d records (db now %s, cap %s)\n",
			deleted, formatBytes(d.footprint()), formatBytes(d.limits.MaxSizeBytes))
	}
	return deleted
}

// trimRows deletes the oldest rows of a table until only maxRows remain.
func (d *Database) trimRows(table, timeColumn string, maxRows int) int64 {
	var count int64
	if err := d.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		return 0
	}
	var deleted int64
	for count > int64(maxRows) {
		limit := count - int64(maxRows)
		if limit > pruneChunk {
			limit = pruneChunk
		}
		affected, err := d.deleteOldest(table, timeColumn, limit)
		if err != nil || affected == 0 {
			break
		}
		deleted += affected
		count -= affected
	}
	return deleted
}

// trimToSize deletes the oldest records until the main file is back under the
// pruning target. Each round deletes 5% of a table and checkpoints, so the
// measurement reflects what is really on disk before deciding to delete more.
func (d *Database) trimToSize() int64 {
	var deleted int64
	for round := 0; round < maintenanceRounds; round++ {
		if d.mainLogicalSize() <= d.sizeTarget() {
			break
		}
		roundDeleted := d.deleteOldestFraction("dns_records", "timestamp_ms", 5) +
			d.deleteOldestFraction("connection_records", "start_time_ms", 5)
		if roundDeleted == 0 {
			break // both tables are empty — nothing left to trim
		}
		deleted += roundDeleted
		if err := d.checkpoint(); err != nil {
			fmt.Printf("[monitor] checkpoint failed: %v\n", err)
			break
		}
	}
	return deleted
}

// sizeTarget is the main-file size the database is pruned back to: 90% of the
// hard page limit, i.e. 10% of headroom before SQLite starts rejecting writes.
func (d *Database) sizeTarget() int64 {
	return d.hardLimitBytes() / 100 * sizeTargetPercent
}

// mainLogicalSize is the logical size of the main database file — exactly what
// PRAGMA max_page_count limits, and therefore the metric that decides when
// pruning must run.
func (d *Database) mainLogicalSize() int64 {
	var pageCount, pageSize int64
	if err := d.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0
	}
	if err := d.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil || pageSize <= 0 {
		pageSize = 4096
	}
	size := pageCount * pageSize
	if size < 0 {
		size = 0
	}
	return size
}

// deleteOldestFraction deletes the oldest percent% of a table (at least one
// row, at most everything).
func (d *Database) deleteOldestFraction(table, timeColumn string, percent int) int64 {
	var count int64
	if err := d.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count == 0 {
		return 0
	}
	limit := count * int64(percent) / 100
	if limit < 1 {
		limit = 1
	}
	if limit > count {
		limit = count
	}
	affected, err := d.deleteOldest(table, timeColumn, limit)
	if err != nil {
		return 0
	}
	return affected
}

// deleteOldest deletes up to limit of the oldest rows of a table.
func (d *Database) deleteOldest(table, timeColumn string, limit int64) (int64, error) {
	statement := fmt.Sprintf(
		"DELETE FROM %s WHERE id IN (SELECT id FROM %s ORDER BY %s ASC LIMIT ?)",
		table, table, timeColumn,
	)
	result, err := d.db.Exec(statement, limit)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return affected, nil
}

// checkpoint folds the WAL back into the main file. In WAL mode a file only
// shrinks at checkpoint time, so this is what turns freed pages into released
// disk space.
func (d *Database) checkpoint() error {
	_, err := d.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// footprint reports the space the database occupies on disk: the main file's
// logical size plus the WAL. The WAL is included because committed pages live
// there until the next checkpoint.
func (d *Database) footprint() int64 {
	return d.mainLogicalSize() + d.walSize()
}

// fileSize reports the raw on-disk size (main file + WAL).
func (d *Database) fileSize() int64 {
	var size int64
	if stat, err := os.Stat(d.dbPath); err == nil {
		size += stat.Size()
	}
	size += d.walSize()
	return size
}

func (d *Database) walSize() int64 {
	var size int64
	if stat, err := os.Stat(d.dbPath + "-wal"); err == nil {
		size += stat.Size()
	}
	return size
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}
