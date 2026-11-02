package cli

// A FILE imported into a managed database the way the console imports it:
// sent in parts to the API the CLI signed in to, which streams them into the
// database's backup store, and imported there by the database itself. Nothing
// to install here; the same rules as the console's (the client restricted to
// the dump's SQL, run as the database's owner, the file told by its bytes),
// a restore point first and the import's report after. `--local`
// (localImport) is the other way: the person's own client, from here.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// partStall is how long a part may go without a byte moving — while it is
// sent, or after, waiting for the API's answer — before it is cut and sent
// again. A connection that stalls holds the import forever otherwise: a part
// has no deadline, since a slow line legitimately takes minutes over one.
var partStall = 2 * time.Minute

// errPartStalled is the cause a stalled part is cut with.
var errPartStalled = errors.New("nothing moved for too long")

// importFileAccepted mirrors the API's rule (src/databases/imports/
// file-formats.ts in the API): which file names an engine imports. The API
// decides; this only refuses before a byte goes.
func importFileAccepted(engine, name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case isSQLServer(engine):
		return namedWith(n, ".bak")
	case engine == "postgres":
		return namedWith(n, ".dump", ".backup", ".pgdump", ".gz", ".sql")
	case isMySQL(engine):
		return namedWith(n, ".gz", ".sql")
	}
	return false
}

// namedWith is whether n ends in one of the extensions, after a name.
func namedWith(n string, exts ...string) bool {
	for _, e := range exts {
		if len(n) > len(e) && strings.HasSuffix(n, e) {
			return true
		}
	}
	return false
}

// importFileNames says what an engine imports, for a refusal.
func importFileNames(engine string) string {
	switch {
	case isSQLServer(engine):
		return "a .bak (BACKUP DATABASE … TO DISK)"
	case engine == "postgres":
		return "a .sql or .sql.gz dump, or pg_dump's custom format (.dump)"
	default:
		return "a .sql or .sql.gz dump (mysqldump, mariadb-dump, another provider's export)"
	}
}

// importFileProblem reads a file's first bytes as the database's server will
// (the appliance's dbtools.Sniff and ErrNotADump): what it then refuses, it
// refuses only once the whole file is uploaded, so the same reasons are said
// here before a byte goes. "" is a file worth sending; the server still
// decides. A .bak is SQL Server's to judge.
func importFileProblem(engine, path string) (string, error) {
	if isSQLServer(engine) {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, sniffBytes)
	kind := sniffFile(r)
	if kind == "gzip" {
		z, err := gzip.NewReader(r)
		if err != nil {
			return "says it is gzipped but is not readable as gzip", nil
		}
		defer z.Close()
		kind = sniffFile(bufio.NewReaderSize(z, sniffBytes))
		if kind == "gzip" {
			return "is gzipped twice: gunzip it once and upload it again", nil
		}
		if kind == "empty" {
			return "is empty once gunzipped", nil
		}
	}
	switch kind {
	case "text":
		return "", nil
	case "pgdump":
		if engine == "postgres" {
			return "", nil
		}
		return "is a PostgreSQL dump (pg_dump's custom format): it imports into a PostgreSQL database", nil
	case "zip":
		return "is a .zip: unzip it and upload the dump inside (a .sql, or a .sql.gz)", nil
	case "tar":
		return "is a tar archive: upload " + importFileNames(engine), nil
	case "utf16":
		return "is UTF-16 text (Windows PowerShell's > writes it so): save it as UTF-8, or have the dump tool write the file itself (mysqldump --result-file, pg_dump -f)", nil
	case "bzip2", "xz", "zstd", "7z":
		return "is compressed with " + kind + ", and only gzip is read: decompress it and upload the .sql, or gzip it (.sql.gz)", nil
	case "bak":
		return "is a SQL Server backup (.bak): it imports into a SQL Server database", nil
	}
	return "is not a dump: its bytes are not SQL text, nor a format a dump comes in", nil
}

// sniffBytes is how much of a file is looked at: enough that a binary file
// all but surely shows a NUL byte, which no SQL dump holds (mysqldump writes
// one as \0, and PostgreSQL's text cannot hold one at all).
const sniffBytes = 8 << 10

// sniffFile is the appliance's dbtools.Sniff: what a file is, from its first
// bytes.
func sniffFile(r *bufio.Reader) string {
	head, _ := r.Peek(sniffBytes)
	switch {
	case len(head) == 0:
		return "empty"
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return "gzip"
	case bytes.HasPrefix(head, []byte("PGDMP")):
		return "pgdump"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return "zip"
	case len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar")):
		return "tar"
	case len(head) >= 10 && bytes.HasPrefix(head, []byte("BZh")) && head[3] >= '1' && head[3] <= '9' && bytes.Equal(head[4:10], []byte("\x31\x41\x59\x26\x53\x59")):
		return "bzip2"
	case bytes.HasPrefix(head, []byte("\xfd7zXZ\x00")):
		return "xz"
	case bytes.HasPrefix(head, []byte("\x28\xb5\x2f\xfd")):
		return "zstd"
	case bytes.HasPrefix(head, []byte("7z\xbc\xaf\x27\x1c")):
		return "7z"
	case bytes.HasPrefix(head, []byte("TAPE")) && bytes.IndexByte(head, 0) >= 0:
		return "bak"
	case utf16Text(head):
		return "utf16"
	case bytes.IndexByte(head, 0) >= 0:
		return "binary"
	}
	return "text"
}

// utf16Text is text written two bytes a character: a byte-order mark, or —
// none written — a NUL in nearly every other byte and hardly any between, as
// ASCII is in UTF-16 (a run of zeros, NUL on both sides, is binary).
func utf16Text(head []byte) bool {
	if bytes.HasPrefix(head, []byte{0xff, 0xfe}) || bytes.HasPrefix(head, []byte{0xfe, 0xff}) {
		return true
	}
	if len(head) < 64 {
		return false
	}
	var even, odd int
	for i, b := range head[:len(head)&^1] {
		if b == 0 {
			if i%2 == 0 {
				even++
			} else {
				odd++
			}
		}
	}
	most, few := len(head)/2*9/10, len(head)/2/10
	return (even >= most && odd <= few) || (odd >= most && even <= few)
}

// importUploadTicket is what the API takes next: the parts it does not hold
// yet, each exactly part_bytes (the last one less). The CLI sends them to the
// API it signed in to (PUT …/parts/<n>), never to the part_urls the ticket
// also lists: those name the API by its own configured address, which need
// not be the one the CLI uses, and the token goes nowhere else.
type importUploadTicket struct {
	PartBytes     int64  `json:"part_bytes"`
	PartCount     int    `json:"part_count"`
	PartNumbers   []int  `json:"part_numbers"`
	ExpiresAt     string `json:"expires_at"`
	UploadedBytes int64  `json:"uploaded_bytes"`
}

type importUpload struct {
	Job    api.DatabaseJob    `json:"job"`
	Upload importUploadTicket `json:"upload"`
}

type fileImportOptions struct {
	// database is what a .bak restores as (SQL Server); a dump goes into the
	// database's own.
	database string
	replace  bool
	detach   bool
}

// fileImport uploads a dump or a .bak and has the database import it (after
// a restore point, so it can be undone). An upload of the same file left
// open by an earlier run carries on where it stopped: the store says what it
// holds, and nothing is sent twice.
func (a *App) fileImport(ctx context.Context, d *api.Database, file string, o fileImportOptions) error {
	name := filepath.Base(file)
	if !importFileAccepted(d.Engine, name) {
		if isSQLServer(d.Engine) {
			return errors.New("a SQL Server database imports a backup file: a .bak (BACKUP DATABASE … TO DISK)")
		}
		return fmt.Errorf("%s imports %s", engineName(d.Engine), importFileNames(d.Engine))
	}
	st, err := os.Stat(file)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: no such file", file)
	}
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory: import one file (a pg_dump directory archive goes in with --local)", file)
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s is empty", file)
	}
	if problem, err := importFileProblem(d.Engine, file); err != nil {
		return err
	} else if problem != "" {
		return fmt.Errorf("%s %s", name, problem)
	}
	c, err := a.Client()
	if err != nil {
		return err
	}
	open, err := a.openUpload(ctx, d.Name)
	if err != nil {
		return err
	}
	target := o.database
	if target == "" {
		target = str(d.Connection.Database)
	}
	var up importUpload
	switch {
	case open == nil:
		body := map[string]interface{}{"file_name": name, "size_bytes": st.Size()}
		if o.database != "" {
			body["database"] = o.database
		}
		if o.replace {
			body["replace"] = true
		}
		if err := c.Do(ctx, "POST", dbPath(d.Name, "/imports/uploads"), nil, body, &up); err != nil {
			return importRefusal(d, err)
		}
	case sameUpload(open, name, st.Size(), target, o.replace):
		if err := c.Do(ctx, "POST", dbPath(d.Name, "/imports/uploads/", open.ID, "/parts"), nil, nil, &up); err != nil {
			return err
		}
		a.printf("Carrying on with the upload of %s: %s of %s already sent.\n", name,
			bytesText(strconv.FormatInt(up.Upload.UploadedBytes, 10)), bytesText(strconv.FormatInt(st.Size(), 10)))
	default:
		return fmt.Errorf("an upload of %s is open on %s (since %s): send that file again to finish it, or `mw db import %s --cancel` to drop it",
			str(open.Source), d.Name, when(open.CreatedAt), d.Name)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	a.printf("Uploading %s (%s) to %s…\n", name, bytesText(strconv.FormatInt(st.Size(), 10)), d.Name)
	if err := a.sendImportParts(ctx, c, d.Name, up.Job.ID, f, st.Size(), up.Upload, name); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("stopped: run the same command again to carry on where it stopped, or `mw db import %s --cancel` to drop the upload", d.Name)
		}
		return fmt.Errorf("%w: run the same command again to carry on where it stopped, or `mw db import %s --cancel`", err, d.Name)
	}
	var job api.DatabaseJob
	if err := c.Do(ctx, "POST", dbPath(d.Name, "/imports/uploads/", up.Job.ID, "/complete"), nil, nil, &job); err != nil {
		return err
	}
	if o.detach {
		return a.print(job, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "Importing %s into %s: `mw db jobs %s` follows it.\n", name, d.Name, d.Name)
		})
	}
	a.printf("Importing %s into %s (a restore point was marked before it, to undo it)…\n", name, d.Name)
	done, err := a.waitJob(ctx, d.Name, job.ID)
	if err != nil {
		return err
	}
	if a.JSON {
		return a.print(done, nil)
	}
	if o.database != "" {
		a.printf("Imported %s into %s, as its database %s.\n", name, d.Name, o.database)
		return nil
	}
	a.printImportReport(d.Name, name, done)
	return nil
}

// importRefusal turns the API's refusal to start an upload into what the
// person can do about it.
func importRefusal(d *api.Database, err error) error {
	var p *api.Problem
	if !errors.As(err, &p) {
		return err
	}
	switch fmt.Sprint(p.Params["reason"]) {
	case "backups_off":
		hint := ""
		if !isSQLServer(d.Engine) {
			hint = fmt.Sprintf(", or import with your own client: `mw db import %s <file> --local`", d.Name)
		}
		return fmt.Errorf("%s has its backups off, and a file is uploaded where they are: turn them on (`mw db update %s --backups`)%s", d.Name, d.Name, hint)
	case "agent_update":
		return fmt.Errorf("%s's server takes a file once it is updated, in its next maintenance window: until then, `mw db import %s <file> --local` (your own client) or --from-url", d.Name, d.Name)
	case "not_empty":
		return fmt.Errorf("%s already holds %v table(s): --replace drops each table the dump makes and makes it again (a restore point is marked first), or import into an empty database", d.Name, p.Params["tables"])
	}
	return err
}

// sendImportParts sends every part the ticket names, uploadParallel at a
// time, with the progress on standard error. After a failure the store is
// asked what it still lacks, and only that is sent again, uploadRetries
// times; a refusal sending again cannot change ends it at once.
func (a *App) sendImportParts(ctx context.Context, c *api.Client, name, job string, f *os.File, size int64, t importUploadTicket, label string) error {
	bar := a.newProgress(label, size)
	defer bar.finish()
	for round := 0; ; round++ {
		bar.set(t.UploadedBytes)
		err := putImportParts(ctx, c, dbPath(name, "/imports/uploads/", job, "/parts/"), f, size, t, bar)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if permanent(err) || round >= uploadRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scaled(time.Duration(1<<round) * time.Second)):
		}
		var next importUpload
		if err := c.Do(ctx, "POST", dbPath(name, "/imports/uploads/", job, "/parts"), nil, nil, &next); err != nil {
			return err
		}
		if t = next.Upload; len(t.PartNumbers) == 0 {
			return nil
		}
	}
}

// putImportParts PUTs the ticket's parts, uploadParallel at a time; the
// first failure is the answer, and one that sending again cannot change
// stops the others.
func putImportParts(ctx context.Context, c *api.Client, prefix string, f *os.File, size int64, t importUploadTicket, bar *progress) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, uploadParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if first == nil {
			first = err
		}
		if permanent(err) {
			cancel()
		}
	}
	for _, n := range t.PartNumbers {
		off := int64(n-1) * t.PartBytes
		length := t.PartBytes
		if off+length > size {
			length = size - off
		}
		if n < 1 || length <= 0 {
			fail(permanentError{fmt.Errorf("part %d is not a part of a %d-byte file", n, size)})
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			mu.Lock()
			defer mu.Unlock()
			if first != nil {
				return first
			}
			return ctx.Err()
		}
		wg.Add(1)
		go func(n int, off, length int64) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := putImportPart(ctx, c, prefix+strconv.Itoa(n), f, off, length, bar); err != nil {
				fail(fmt.Errorf("part %d: %w", n, err))
			}
		}(n, off, length)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return first
}

// putImportPart sends one part to the API, cut when nothing moves for
// partStall. What it sent comes off the progress if it fails.
func putImportPart(ctx context.Context, c *api.Client, path string, f *os.File, off, n int64, bar *progress) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r := &countingReaderAt{ra: f, bar: bar}
	r.touch()
	stall := partStall
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(stall / 8)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if r.idle() > stall {
					cancel(errPartStalled)
					return
				}
			}
		}
	}()
	err := c.SendSection(ctx, http.MethodPut, path, r, off, n, "application/octet-stream", nil)
	close(stop)
	<-stopped
	if err == nil {
		return nil
	}
	bar.add(-r.sent.Load())
	if errors.Is(context.Cause(ctx), errPartStalled) {
		return errPartStalled
	}
	return err
}

// countingReaderAt counts what a part has read, for the progress bar and
// for knowing when one stalled.
type countingReaderAt struct {
	ra   io.ReaderAt
	bar  *progress
	sent atomic.Int64
	last atomic.Int64 // when a byte last moved, in Unix nanoseconds
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	k, err := c.ra.ReadAt(p, off)
	if k > 0 {
		c.sent.Add(int64(k))
		c.bar.add(int64(k))
		c.touch()
	}
	return k, err
}

func (c *countingReaderAt) touch() { c.last.Store(time.Now().UnixNano()) }

func (c *countingReaderAt) idle() time.Duration {
	return time.Since(time.Unix(0, c.last.Load()))
}

// permanentError is a failure sending again cannot change.
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// permanent is a refusal sending again cannot change: the API said no — a
// 4xx other than a timeout or too many requests, such as an upload given up
// or expired — or the part is not one of the file's.
func permanent(err error) bool {
	if errors.Is(err, api.ErrUnauthorized) {
		return true
	}
	var pe permanentError
	if errors.As(err, &pe) {
		return true
	}
	var p *api.Problem
	if errors.As(err, &p) {
		return p.Status >= 400 && p.Status < 500 && p.Status != http.StatusRequestTimeout && p.Status != http.StatusTooManyRequests
	}
	return false
}

// openUpload is the database's import still waiting for its file, if any.
func (a *App) openUpload(ctx context.Context, name string) (*api.DatabaseJob, error) {
	jobs, err := a.jobs(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].Kind == "import" && jobs[i].Status == "uploading" {
			return &jobs[i], nil
		}
	}
	return nil, nil
}

// sameUpload is whether an open upload is this file going to the same place:
// its name, its size, the database it goes into and --replace.
func sameUpload(j *api.DatabaseJob, file string, size int64, database string, replace bool) bool {
	if str(j.Source) != file || j.SizeBytes == nil || *j.SizeBytes != strconv.FormatInt(size, 10) {
		return false
	}
	if j.Database != nil && database != "" && !strings.EqualFold(*j.Database, database) {
		return false
	}
	return j.Replace == nil || *j.Replace == replace
}

// cancelImportUpload drops the database's open upload: what was sent of it
// is deleted, and nothing is imported.
func (a *App) cancelImportUpload(ctx context.Context, d *api.Database) error {
	open, err := a.openUpload(ctx, d.Name)
	if err != nil {
		return err
	}
	if open == nil {
		return fmt.Errorf("%s has no upload open", d.Name)
	}
	c, err := a.Client()
	if err != nil {
		return err
	}
	if err := c.Do(ctx, "DELETE", dbPath(d.Name, "/imports/uploads/", open.ID), nil, nil, nil); err != nil {
		return err
	}
	a.printf("Dropped the upload of %s: nothing was imported.\n", str(open.Source))
	return nil
}

// printImportReport says what an import did: the tables it left, and what of
// the source was left out and why — the same report for a file and a URL.
func (a *App) printImportReport(name, what string, j *api.DatabaseJob) {
	r := j.Report
	if r == nil {
		a.printf("Imported %s into %s.\n", what, name)
		return
	}
	a.printf("Imported %s into %s: %d table(s).\n", what, name, r.Tables)
	if r.LeftOut > 0 {
		a.printf("%d statement(s) could not run and were left out:\n", r.LeftOut)
		for _, n := range r.Notes {
			a.printf("  %s\n", n)
		}
	}
	if r.DefinersRemoved > 0 {
		a.printf("Left out %d DEFINER clause(s): those views, routines and triggers now run as the owner.\n", r.DefinersRemoved)
	}
	if r.ServerStatementsRemoved > 0 {
		a.printf("Left out %d statement(s) only the source server could run (its GTIDs, its own settings).\n", r.ServerStatementsRemoved)
	}
	if r.OtherDatabase != nil && *r.OtherDatabase != "" {
		a.printf("The dump named its own database, %s: it went into %s's instead.\n", *r.OtherDatabase, name)
	}
}
