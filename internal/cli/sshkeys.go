package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// sshKeysCmd: the account's SSH keys, put on an instance when it is created
// or reinstalled (`--ssh-key`), so it is reached with a key, not a password.
func sshKeysCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ssh-keys",
		Aliases: []string{"ssh-key", "keys-ssh"},
		Short:   "SSH keys put on instances when they are created or reinstalled",
		Example: "  mw ssh-keys\n" +
			"  mw ssh-keys add                       # ~/.ssh/id_ed25519.pub\n" +
			"  mw ssh-keys add ~/.ssh/work.pub --name work\n" +
			"  mw instances create --plan c2m4 --image ubuntu-24-04 --ssh-key work",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			keys, err := a.sshKeys(cmd.Context())
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "TYPE", "FINGERPRINT", "ADDED"}, empty: "No SSH keys yet: `mw ssh-keys add`."}
			for _, k := range keys {
				l.add(k.ID, k.ID, k.Name, k.Type, k.Fingerprint, when(k.CreatedAt))
			}
			return a.printList(keys, l)
		},
	}

	var name string
	add := &cobra.Command{
		Use:   "add [public-key-file]",
		Short: "Add a public key (default: ~/.ssh/id_ed25519.pub, id_ecdsa.pub or id_rsa.pub)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := ""
			if len(args) == 1 {
				file = args[0]
			}
			line, path, err := readPublicKey(file)
			if err != nil {
				return err
			}
			if name == "" {
				name = keyName(line, path)
			}
			k, err := a.addSSHKey(cmd.Context(), name, line)
			if err != nil {
				return err
			}
			return a.printDetail(k, keyDetail(*k, "SSH key added."))
		},
	}
	add.Flags().StringVar(&name, "name", "", "what to call it (default: the key's comment, or the file name)")

	get := &cobra.Command{
		Use:   "get <key>",
		Short: "An SSH key and its public half",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := a.sshKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printDetail(k, keyDetail(*k, ""))
		},
	}

	rename := &cobra.Command{
		Use:   "rename <key> <new-name>",
		Short: "Rename an SSH key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := a.sshKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.SSHKey
			if err := c.Do(cmd.Context(), "PATCH", "/ssh-keys/"+k.ID, nil, map[string]string{"name": args[1]}, &out); err != nil {
				return notOffered(err, "SSH keys")
			}
			return a.printDetail(out, keyDetail(out, "SSH key renamed."))
		},
	}

	var yes bool
	remove := &cobra.Command{
		Use:     "delete <key>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete an SSH key (instances that have it keep it)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := a.sshKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Delete the SSH key %s (%s)? Instances that have it keep it. [y/N] ", k.Name, k.Fingerprint), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing deleted.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/ssh-keys/"+k.ID, nil, nil, nil); err != nil {
				return notOffered(err, "SSH keys")
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": k.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "SSH key %s deleted.\n", k.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")

	cmd.AddCommand(add, get, rename, remove)
	return cmd
}

func keyDetail(k api.SSHKey, headline string) detail {
	var d detail
	d.add("ID", k.ID)
	d.add("Name", k.Name)
	d.add("Type", k.Type)
	d.add("Fingerprint", k.Fingerprint)
	if k.CreatedAt != "" {
		d.add("Added", when(k.CreatedAt))
	}
	if k.PublicKey != "" {
		d.after = append(d.after, "", k.PublicKey)
	}
	if headline != "" {
		d.after = append([]string{headline}, d.after...)
	}
	return d
}

func (a *App) sshKeys(ctx context.Context) ([]api.SSHKey, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	keys, err := api.All[api.SSHKey](ctx, c, "/ssh-keys", nil)
	return keys, notOffered(err, "SSH keys")
}

// sshKey finds a key by id, name or fingerprint.
func (a *App) sshKey(ctx context.Context, ref string) (*api.SSHKey, error) {
	keys, err := a.sshKeys(ctx)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.ID == ref || strings.EqualFold(k.Name, ref) || k.Fingerprint == ref || strings.TrimPrefix(k.Fingerprint, "SHA256:") == ref {
			k := k
			return &k, nil
		}
	}
	return nil, fmt.Errorf("no SSH key %s (`mw ssh-keys` lists them)", ref)
}

func (a *App) addSSHKey(ctx context.Context, name, line string) (*api.SSHKey, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var out api.SSHKey
	if err := c.Do(ctx, "POST", "/ssh-keys", nil, map[string]string{"name": name, "public_key": line}, &out); err != nil {
		return nil, notOffered(err, "SSH keys")
	}
	return &out, nil
}

// resolveSSHKeys turns --ssh-key values into key ids: an id, a name or a
// fingerprint of a key in the account, or a public key file — added to the
// account first when it is not there yet.
func (a *App) resolveSSHKeys(ctx context.Context, refs []string) ([]string, error) {
	refs = splitList(refs)
	if len(refs) == 0 {
		return nil, nil
	}
	keys, err := a.sshKeys(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ref := range refs {
		if looksLikeFile(ref) {
			line, path, err := readPublicKey(ref)
			if err != nil {
				return nil, err
			}
			fp, err := fingerprint(line)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			id := ""
			for _, k := range keys {
				if k.Fingerprint == fp {
					id = k.ID
				}
			}
			if id == "" {
				k, err := a.addSSHKey(ctx, keyName(line, path), line)
				if err != nil {
					return nil, err
				}
				if !a.JSON {
					fmt.Fprintf(a.Err, "Added %s to your SSH keys as %s.\n", path, k.Name)
				}
				keys = append(keys, *k)
				id = k.ID
			}
			ids = append(ids, id)
			continue
		}
		found := ""
		for _, k := range keys {
			if k.ID == ref || strings.EqualFold(k.Name, ref) || k.Fingerprint == ref {
				found = k.ID
			}
		}
		if found == "" {
			return nil, fmt.Errorf("no SSH key %s: `mw ssh-keys` lists them, or pass a .pub file", ref)
		}
		ids = append(ids, found)
	}
	return ids, nil
}

func looksLikeFile(ref string) bool {
	if strings.HasSuffix(ref, ".pub") || strings.HasPrefix(ref, "~/") || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "../") {
		return true
	}
	_, err := os.Stat(ref)
	return err == nil && !digitsRe.MatchString(ref)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// readPublicKey reads the first key line of a .pub file (default: the usual
// ones in ~/.ssh) and refuses a private key.
func readPublicKey(file string) (line, path string, err error) {
	candidates := []string{file}
	if file == "" {
		candidates = []string{"~/.ssh/id_ed25519.pub", "~/.ssh/id_ecdsa.pub", "~/.ssh/id_rsa.pub"}
	}
	for _, c := range candidates {
		p := expandHome(c)
		raw, err := os.ReadFile(p)
		if err != nil {
			if file != "" {
				return "", "", err
			}
			continue
		}
		text := strings.TrimSpace(string(raw))
		if strings.Contains(text, "PRIVATE KEY") {
			return "", "", fmt.Errorf("%s is a PRIVATE key: pass the .pub file next to it", p)
		}
		for _, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				if _, err := fingerprint(l); err != nil {
					return "", "", fmt.Errorf("%s: %w", p, err)
				}
				return l, p, nil
			}
		}
		return "", "", fmt.Errorf("%s has no public key", p)
	}
	return "", "", errors.New("no public key in ~/.ssh (id_ed25519.pub, id_ecdsa.pub, id_rsa.pub): create one with `ssh-keygen -t ed25519`, or pass the file")
}

// fingerprint is OpenSSH's SHA256 fingerprint of a public key line.
func fingerprint(line string) (string, error) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return "", errors.New("not an OpenSSH public key (expected `ssh-ed25519 AAAA… comment`)")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return "", errors.New("not an OpenSSH public key: its key part is not base64")
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// keyName is a key's comment ("ana@laptop"), else its file name.
func keyName(line, path string) string {
	f := strings.Fields(line)
	if len(f) >= 3 {
		return strings.Join(f[2:], " ")
	}
	return strings.TrimSuffix(filepath.Base(path), ".pub")
}
