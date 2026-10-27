package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Custom images: the account's own disk images and ISOs, uploaded straight
// to storage in parts. `mw images` stays the catalog.

const (
	uploadParallel = 4 // parts in flight at once
	uploadRetries  = 4 // rounds of "what is still missing?" after a failure
)

// partClient PUTs the parts to their presigned URLs: a plain client, so the
// API token never travels to storage.
var partClient = &http.Client{}

func customImagesCmd(a *App) *cobra.Command {
	var kind string
	list := func(cmd *cobra.Command, _ []string) error { return a.listCustomImages(cmd.Context(), kind) }
	cmd := &cobra.Command{
		Use:     "custom-images",
		Aliases: []string{"custom-image", "my-images"},
		Short:   "Your own disk images and ISOs: upload, list, change, delete",
		Long: "Your own images. A disk image (qcow2, raw, img, vmdk) builds instances —\n" +
			"`mw instances create --custom-image <name>` — and an ISO boots an installer. An upload\n" +
			"goes straight to storage in parts, resumes when interrupted, and is checked before use.\n" +
			"`mw images` lists the catalog.",
		Example: "  mw custom-images\n" +
			"  mw custom-images upload debian-12.qcow2 --name debian-12 --wait\n" +
			"  mw custom-images update debian-12 --cloud-init --default-username debian\n" +
			"  mw instances create --plan c2m4 --custom-image debian-12",
		Args: cobra.NoArgs,
		RunE: list,
	}
	cmd.Flags().StringVar(&kind, "kind", "", "disk or iso")
	ls := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "Your custom images", Args: cobra.NoArgs, RunE: list}
	ls.Flags().StringVar(&kind, "kind", "", "disk or iso")

	get := &cobra.Command{
		Use:   "get <image>",
		Short: "A custom image: its state, size, how it boots, where it is ready",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, err := a.customImage(cmd.Context(), args[0])
			if err != nil {
				return notOffered(err, "Custom images from the API")
			}
			return a.printDetail(img, customImageDetail(*img))
		},
	}

	quota := &cobra.Command{
		Use:   "quota",
		Short: "How many images, how much storage and how large a file the account may upload",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := a.imageQuota(cmd.Context())
			if err != nil {
				return notOffered(err, "Custom images from the API")
			}
			var d detail
			d.add("Images", fmt.Sprintf("%d of %d", q.Images, q.MaxImages))
			d.add("Storage", bytesText(q.TotalBytes)+" of "+bytesText(q.MaxTotalBytes))
			d.add("Largest file", bytesText(q.MaxFileBytes))
			d.add("Kept", plural(q.RetentionDays, "day", "days")+" unless renewed")
			d.add("Uploads", map[bool]string{true: "on", false: "off"}[q.UploadsEnabled])
			d.add("File types", orDash(strings.Join(q.AcceptedExtensions, " ")))
			return a.printDetail(q, d)
		},
	}

	var name, resume string
	var wait bool
	var timeout time.Duration
	upload := &cobra.Command{
		Use:   "upload <file>",
		Short: "Upload a disk image or an ISO (in parts, four at a time; it resumes when interrupted)",
		Example: "  mw custom-images upload debian-12.qcow2 --wait\n" +
			"  mw custom-images upload win2022.iso --name windows-2022\n" +
			"  mw custom-images upload debian-12.qcow2 --resume 0193f0e2-…   # one that stopped",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() || st.Size() == 0 {
				return fmt.Errorf("%s is not a file with something in it", args[0])
			}
			size, filename := st.Size(), filepath.Base(args[0])
			c, err := a.Client()
			if err != nil {
				return err
			}
			var up api.CustomImageUpload
			if resume != "" {
				if err := c.Do(ctx, "GET", "/images/"+url.PathEscape(resume)+"/upload", nil, nil, &up); err != nil {
					return notOffered(err, "Custom images from the API")
				}
			} else {
				if err := a.checkImageQuota(ctx, filename, size); err != nil {
					return err
				}
				body := map[string]interface{}{"filename": filename, "size_bytes": size}
				if name != "" {
					body["name"] = name
				}
				if err := c.Do(ctx, "POST", "/images", nil, body, &up); err != nil {
					return notOffered(err, "Custom images from the API")
				}
			}
			if err := checkParts(up, size); err != nil {
				return err
			}
			if err := a.uploadImage(ctx, c, f, size, &up, filename); err != nil {
				return fmt.Errorf("%w\nThe upload can go on where it stopped: `mw custom-images upload %s --resume %s` (or drop it: `mw custom-images abort %s`)",
					err, shellQuote(args[0]), up.ImageID, up.ImageID)
			}
			var acc api.OperationAccepted
			if err := c.Do(ctx, "POST", "/images/"+up.ImageID+"/upload/complete", nil, nil, &acc); err != nil {
				return err
			}
			if !wait {
				switch {
				case a.JSON:
					return a.encode(acc)
				case a.Quiet:
					fmt.Fprintln(a.Out, up.ImageID)
				default:
					fmt.Fprintf(a.Out, "Uploaded %s (image %s). It is checked and converted next: `mw custom-images get %s` (or --wait).\n", filename, up.ImageID, up.ImageID)
				}
				return nil
			}
			img, err := a.waitImage(ctx, up.ImageID, timeout)
			if err != nil {
				return err
			}
			switch {
			case a.JSON:
				return a.encode(img)
			case a.Quiet:
				fmt.Fprintln(a.Out, img.ID)
			case img.Kind == "iso":
				fmt.Fprintf(a.Out, "ISO %s (%s) is ready.\n", img.Name, img.ID)
			default:
				fmt.Fprintf(a.Out, "Image %s (%s) is ready: `mw instances create --plan <plan> --custom-image %s`.\n", img.Name, img.ID, img.Name)
			}
			return nil
		},
	}
	upload.Flags().StringVar(&name, "name", "", "what to call it (default: the file's name)")
	upload.Flags().StringVar(&resume, "resume", "", "an upload that stopped, by its image id: send what is missing")
	waitFlags(upload, &wait, &timeout, time.Hour)

	var newName, username string
	var cloudInit, efi, tpm bool
	update := &cobra.Command{
		Use:   "update <image>",
		Short: "Rename a custom image, or say how it boots (cloud-init, EFI, TPM, default user)",
		Example: "  mw custom-images update debian-12 --cloud-init --default-username debian\n" +
			"  mw custom-images update win2022 --efi --tpm\n" +
			"  mw custom-images update old-image --cloud-init=false",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]interface{}{}
			fl := cmd.Flags()
			if fl.Changed("name") {
				body["name"] = newName
			}
			if fl.Changed("default-username") {
				if username == "" {
					body["default_username"] = nil
				} else {
					body["default_username"] = username
				}
			}
			for flag, field := range map[string]string{"cloud-init": "has_cloud_init", "efi": "requires_efi", "tpm": "requires_tpm"} {
				if fl.Changed(flag) {
					body[field] = map[string]bool{"cloud-init": cloudInit, "efi": efi, "tpm": tpm}[flag]
				}
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --name, --default-username, --cloud-init, --efi or --tpm")
			}
			return a.patchCustomImage(cmd.Context(), args[0], body)
		},
	}
	update.Flags().StringVar(&newName, "name", "", "a new name")
	update.Flags().StringVar(&username, "default-username", "", "the user SSH keys and the password are set for (\"\" clears it)")
	update.Flags().BoolVar(&cloudInit, "cloud-init", false, "it runs cloud-init, so SSH keys, user data and the password reach it")
	update.Flags().BoolVar(&efi, "efi", false, "it boots with UEFI")
	update.Flags().BoolVar(&tpm, "tpm", false, "it needs a TPM (Windows 11 and Server 2025 do)")

	rename := &cobra.Command{
		Use:   "rename <image> <new-name>",
		Short: "Rename a custom image",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.patchCustomImage(cmd.Context(), args[0], map[string]interface{}{"name": args[1]})
		},
	}

	var yesAbort bool
	abort := &cobra.Command{
		Use:   "abort <image>",
		Short: "Drop an upload that has not finished, and what of it arrived",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, err := a.customImage(cmd.Context(), args[0])
			if err != nil {
				return notOffered(err, "Custom images from the API")
			}
			if !yesAbort {
				ok, err := a.confirm(fmt.Sprintf("Drop the upload of %s? [y/N] ", img.Name), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing changed.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "POST", "/images/"+img.ID+"/upload/abort", nil, nil, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": img.ID, "aborted": true})
			}
			fmt.Fprintf(a.Out, "Upload of %s dropped.\n", img.Name)
			return nil
		},
	}
	abort.Flags().BoolVarP(&yesAbort, "yes", "y", false, "drop it without asking")

	var yes bool
	remove := &cobra.Command{
		Use:     "delete <image>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a custom image (you type its name to confirm)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, err := a.customImage(cmd.Context(), args[0])
			if err != nil {
				return notOffered(err, "Custom images from the API")
			}
			if !yes {
				q := fmt.Sprintf("Delete custom image %s (%s)? It cannot be recovered.", img.Name, img.ID)
				if img.InstanceCount > 0 {
					q += fmt.Sprintf(" Instances built from it: %d.", img.InstanceCount)
				}
				q += fmt.Sprintf("\nType its name (%s) to confirm: ", img.Name)
				if err := a.typeToConfirm(q, img.Name, "--yes"); err != nil {
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/images/"+img.ID, nil, nil, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": img.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "Custom image %s deleted.\n", img.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")

	cmd.AddCommand(ls, get, quota, upload, update, rename, abort, remove)
	return cmd
}

func (a *App) listCustomImages(ctx context.Context, kind string) error {
	q := url.Values{}
	switch strings.ToLower(kind) {
	case "":
	case "disk", "disk_image", "disk-image":
		q.Set("kind", "disk_image")
	case "iso":
		q.Set("kind", "iso")
	default:
		return errors.New("--kind is disk or iso")
	}
	c, err := a.Client()
	if err != nil {
		return err
	}
	var list struct {
		Data []api.CustomImage `json:"data"`
	}
	if err := c.Do(ctx, "GET", "/images", q, nil, &list); err != nil {
		return notOffered(err, "Custom images from the API")
	}
	l := listing{headers: []string{"ID", "NAME", "KIND", "STATUS", "SIZE", "CLOUD-INIT", "INSTANCES", "KEPT UNTIL"},
		empty: "No custom images yet: `mw custom-images upload <file>`."}
	for _, img := range list.Data {
		size, ci := "—", "—"
		if img.SizeBytes != nil {
			size = bytesText(*img.SizeBytes)
		}
		if img.Kind == "disk_image" {
			ci = yesNo(img.HasCloudInit)
		}
		l.add(img.ID, img.ID, img.Name, imageKind(img.Kind), imageStatus(img), size, ci, strconv.Itoa(img.InstanceCount), whenPtr(img.ExpiresAt))
	}
	return a.printList(list.Data, l)
}

func (a *App) patchCustomImage(ctx context.Context, ref string, body map[string]interface{}) error {
	img, err := a.customImage(ctx, ref)
	if err != nil {
		return notOffered(err, "Custom images from the API")
	}
	c, _ := a.Client()
	var out api.CustomImage
	if err := c.Do(ctx, "PATCH", "/images/"+img.ID, nil, body, &out); err != nil {
		return err
	}
	return a.printDetail(out, customImageDetail(out))
}

// customImage finds one of the account's images by id, name or file name.
// Errors are the API's as they came, for customImageID to tell an API without
// the images routes.
func (a *App) customImage(ctx context.Context, ref string) (*api.CustomImage, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	if uuidRe.MatchString(ref) {
		var img api.CustomImage
		if err := c.Do(ctx, "GET", "/images/"+ref, nil, nil, &img); err != nil {
			return nil, err
		}
		return &img, nil
	}
	var list struct {
		Data []api.CustomImage `json:"data"`
	}
	if err := c.Do(ctx, "GET", "/images", nil, nil, &list); err != nil {
		return nil, err
	}
	var found []api.CustomImage
	for _, img := range list.Data {
		if strings.EqualFold(img.Name, ref) || img.Filename == ref {
			found = append(found, img)
		}
	}
	switch len(found) {
	case 1:
		return &found[0], nil
	case 0:
		return nil, fmt.Errorf("no custom image %s (`mw custom-images` lists them)", ref)
	}
	return nil, fmt.Errorf("%s names %d custom images: give the id (`mw custom-images` lists them)", ref, len(found))
}

// customImageID is what --custom-image sends: a ready disk image's id, found
// by id or name. An API without the images routes gets an id as it was given.
func (a *App) customImageID(ctx context.Context, ref string) (string, error) {
	img, err := a.customImage(ctx, ref)
	switch {
	case err != nil && routeMissing(err) && (uuidRe.MatchString(ref) || digitsRe.MatchString(ref)):
		return ref, nil
	case err != nil:
		return "", notOffered(err, "Custom images from the API")
	case img.Kind == "iso":
		return "", fmt.Errorf("%s is an ISO: --custom-image takes a disk image", img.Name)
	}
	if final, ready := imageFinal(img.Status); !ready {
		if final {
			return "", fmt.Errorf("custom image %s is %s: %s", img.Name, img.Status, firstNonEmpty(str(img.FailureReason), "it cannot be used"))
		}
		return "", fmt.Errorf("custom image %s is not ready yet (%s): `mw custom-images get %s` says when it is", img.Name, img.Status, img.Name)
	}
	return img.ID, nil
}

func (a *App) imageQuota(ctx context.Context) (*api.CustomImageQuota, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var q api.CustomImageQuota
	if err := c.Do(ctx, "GET", "/images/quota", nil, nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// checkImageQuota refuses, before anything is sent, an upload the account's
// quota would refuse.
func (a *App) checkImageQuota(ctx context.Context, filename string, size int64) error {
	q, err := a.imageQuota(ctx)
	if err != nil {
		if routeMissing(err) {
			return nil
		}
		return notOffered(err, "Custom images from the API")
	}
	if !q.UploadsEnabled {
		return errors.New("uploading custom images is switched off for this account")
	}
	if ext := strings.ToLower(filepath.Ext(filename)); len(q.AcceptedExtensions) > 0 && !containsFold(q.AcceptedExtensions, ext) {
		return fmt.Errorf("%s: the API takes %s files", filename, strings.Join(q.AcceptedExtensions, ", "))
	}
	if max, ok := new(big.Int).SetString(q.MaxFileBytes, 10); ok && max.Sign() > 0 && big.NewInt(size).Cmp(max) > 0 {
		return fmt.Errorf("%s is %s: the largest file the API takes is %s", filename, bytesText(strconv.FormatInt(size, 10)), bytesText(q.MaxFileBytes))
	}
	if q.MaxImages > 0 && q.Images >= q.MaxImages {
		return fmt.Errorf("the account has %d custom images, the most it may keep: delete one first (`mw custom-images`)", q.Images)
	}
	used, okUsed := new(big.Int).SetString(q.TotalBytes, 10)
	max, okMax := new(big.Int).SetString(q.MaxTotalBytes, 10)
	if okUsed && okMax && max.Sign() > 0 && new(big.Int).Add(used, big.NewInt(size)).Cmp(max) > 0 {
		return fmt.Errorf("%s does not fit: %s of the %s the account may store is in use", filename, bytesText(q.TotalBytes), bytesText(q.MaxTotalBytes))
	}
	return nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// checkParts refuses an upload whose parts do not make this file: a resume of
// another one, say.
func checkParts(up api.CustomImageUpload, size int64) error {
	if up.ImageID == "" || up.PartSizeBytes <= 0 || up.PartCount <= 0 {
		return errors.New("the API did not say how to send the file (no parts)")
	}
	if want := int((size + up.PartSizeBytes - 1) / up.PartSizeBytes); want != up.PartCount {
		return fmt.Errorf("the upload is in %d parts of %s, and this file makes %d: is it the file the upload started with?",
			up.PartCount, bytesText(strconv.FormatInt(up.PartSizeBytes, 10)), want)
	}
	for _, p := range up.Parts {
		if p.PartNumber < 1 || p.PartNumber > up.PartCount {
			return fmt.Errorf("the API sent part %d of %d", p.PartNumber, up.PartCount)
		}
	}
	return nil
}

// uploadImage sends the parts still missing and, after a failure, asks which
// are still missing (with fresh URLs) and sends those, a few times over.
func (a *App) uploadImage(ctx context.Context, c *api.Client, f *os.File, size int64, up *api.CustomImageUpload, label string) error {
	bar := a.newProgress(label, size)
	defer bar.finish()
	for round := 0; ; round++ {
		bar.set(up.UploadedBytes)
		err := uploadParts(ctx, f, size, up, bar)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || round >= uploadRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scaled(time.Duration(1<<round) * time.Second)):
		}
		var next api.CustomImageUpload
		if c.Do(ctx, "GET", "/images/"+up.ImageID+"/upload", nil, nil, &next) == nil && checkParts(next, size) == nil {
			*up = next
		}
	}
}

// uploadParts PUTs the parts, uploadParallel at a time; the first failure is
// the answer (the others are asked for again).
func uploadParts(ctx context.Context, f *os.File, size int64, up *api.CustomImageUpload, bar *progress) error {
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
	}
	for _, p := range up.Parts {
		off := int64(p.PartNumber-1) * up.PartSizeBytes
		n := up.PartSizeBytes
		if off+n > size {
			n = size - off
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		go func(p api.CustomImagePart) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := putPart(ctx, f, off, n, p.URL, bar); err != nil {
				fail(fmt.Errorf("part %d: %w", p.PartNumber, err))
			}
		}(p)
	}
	wg.Wait()
	return first
}

// putPart PUTs one part's bytes, as they are, to its presigned URL.
func putPart(ctx context.Context, f *os.File, off, n int64, target string, bar *progress) error {
	body := &countingReader{r: io.NewSectionReader(f, off, n), bar: bar}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
	if err != nil {
		return err
	}
	req.ContentLength = n
	res, err := partClient.Do(req)
	if err != nil {
		bar.add(-body.sent.Load())
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		bar.add(-body.sent.Load())
		return fmt.Errorf("storage answered %s", res.Status)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// countingReader counts what a part has sent, for the progress bar.
type countingReader struct {
	r    io.Reader
	sent atomic.Int64
	bar  *progress
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.sent.Add(int64(k))
	c.bar.add(int64(k))
	return k, err
}

// progress shows an upload on standard error: a bar on a terminal, a line
// every 10% elsewhere (a CI log), nothing under -q.
type progress struct {
	mu    sync.Mutex
	w     io.Writer
	tty   bool
	off   bool
	label string
	total int64
	done  int64
	start time.Time
	base  int64 // what was already there: it does not count toward the speed
	drawn time.Time
	step  int64
}

func (a *App) newProgress(label string, total int64) *progress {
	p := &progress{w: a.Err, off: a.Quiet, label: label, total: total, start: time.Now(), step: -1}
	if f, ok := a.Err.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.tty = true
	}
	return p
}

func (p *progress) set(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done, p.base, p.start = n, n, time.Now()
	p.draw(false)
}

func (p *progress) add(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += n
	if p.done < 0 {
		p.done = 0
	}
	if p.done > p.total {
		p.done = p.total
	}
	p.draw(false)
}

func (p *progress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.draw(true)
	if p.tty && !p.off {
		fmt.Fprintln(p.w)
	}
}

func (p *progress) draw(force bool) {
	if p.off || p.total <= 0 {
		return
	}
	pct := p.done * 100 / p.total
	if !p.tty {
		if step := pct / 10; step > p.step {
			p.step = step
			fmt.Fprintf(p.w, "Uploading %s: %d%% (%s of %s)\n", p.label, pct, bytesText(strconv.FormatInt(p.done, 10)), bytesText(strconv.FormatInt(p.total, 10)))
		}
		return
	}
	now := time.Now()
	if !force && now.Sub(p.drawn) < 200*time.Millisecond {
		return
	}
	p.drawn = now
	const width = 24
	fill := int(pct * width / 100)
	bar := strings.Repeat("=", fill) + strings.Repeat(" ", width-fill)
	speed := ""
	if secs := now.Sub(p.start).Seconds(); secs > 1 && p.done > p.base {
		speed = "  " + bytesText(strconv.FormatInt(int64(float64(p.done-p.base)/secs), 10)) + "/s"
	}
	fmt.Fprintf(p.w, "\r%s [%s] %3d%%  %s / %s%s   ", p.label, bar, pct, bytesText(strconv.FormatInt(p.done, 10)), bytesText(strconv.FormatInt(p.total, 10)), speed)
}

// imageFinal says whether a custom image's status is the last one, and
// whether that is ready.
func imageFinal(status string) (final, ready bool) {
	switch status {
	case "ready":
		return true, true
	case "failed", "expired", "deleted":
		return true, false
	}
	return false, false
}

// waitImage follows an image through its checks and conversion until it is
// ready, or failed.
func (a *App) waitImage(ctx context.Context, id string, timeout time.Duration) (*api.CustomImage, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		var img api.CustomImage
		err := c.Do(ctx, "GET", "/images/"+id, nil, nil, &img)
		if err == nil {
			if final, ready := imageFinal(img.Status); final {
				if !ready {
					return &img, fmt.Errorf("custom image %s %s: %s", img.Name, img.Status, firstNonEmpty(str(img.FailureReason), "the API gave no reason"))
				}
				return &img, nil
			}
			if line := imageStatus(img); line != last && !a.JSON && !a.Quiet {
				fmt.Fprintf(a.Err, "  image %s: %s\n", img.Name, line)
				last = line
			}
		} else if ctx.Err() == nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("custom image %s is not ready after %s: `mw custom-images get %s` says where it is", id, timeout, id)
		case <-time.After(pollEvery):
		}
	}
}

func customImageDetail(img api.CustomImage) detail {
	var d detail
	d.add("ID", img.ID)
	d.add("Name", img.Name)
	d.add("File", img.Filename)
	d.add("Kind", imageKind(img.Kind))
	status := imageStatus(img)
	if img.FailureReason != nil && *img.FailureReason != "" {
		status += ": " + *img.FailureReason
	}
	d.add("Status", status)
	if img.SizeBytes != nil {
		d.add("Size", bytesText(*img.SizeBytes))
	}
	if img.VirtualSizeBytes != nil {
		d.add("Disk size", bytesText(*img.VirtualSizeBytes))
	}
	if img.MinDiskGB != nil {
		d.add("Smallest disk", fnum(*img.MinDiskGB)+" GB")
	}
	if img.Format != nil {
		d.add("Format", *img.Format)
	}
	if img.Kind == "disk_image" {
		boot := "BIOS"
		if img.RequiresEFI {
			boot = "UEFI"
		}
		if img.RequiresTPM {
			boot += ", TPM"
		}
		d.add("Boot", boot)
		ci := "no: SSH keys, user data and the password do not reach it"
		if img.HasCloudInit {
			ci = "yes"
		}
		d.add("cloud-init", ci)
		d.add("Default user", orDashPtr(img.DefaultUsername))
	}
	if len(img.Regions) > 0 {
		var rs []string
		for _, r := range img.Regions {
			rs = append(rs, "region "+firstNonEmpty(str(r.RegionID), "?")+": "+r.State)
		}
		d.add("Regions", strings.Join(rs, ", "))
	}
	d.add("Instances", strconv.Itoa(img.InstanceCount))
	if img.SHA256 != nil {
		d.add("SHA-256", *img.SHA256)
	}
	if img.ExpiresAt != nil {
		d.add("Kept until", whenPtr(img.ExpiresAt))
	}
	d.add("Created", when(img.CreatedAt))
	return d
}

func imageKind(k string) string {
	if k == "iso" {
		return "ISO"
	}
	return "disk image"
}

// imageStatus is the status, with how far along it is while it is moving.
func imageStatus(img api.CustomImage) string {
	if final, _ := imageFinal(img.Status); !final && img.Progress != nil {
		return fmt.Sprintf("%s %.0f%%", img.Status, *img.Progress)
	}
	return img.Status
}
