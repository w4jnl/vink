package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/adminapi"
	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/logging"
	"github.com/w4jnl/vink/internal/secrets"
	"github.com/w4jnl/vink/internal/service"
)

// adminScope is the scope of a person with shell access to the server
// host: instance admin, no project.
var adminScope = domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "cli:admin"}

// adminFlags say where vink admin runs: on the server host against the
// database file (--db, --config), or anywhere through a context whose key
// is an instance admin key (--context).
type adminFlags struct {
	g       *globals
	server  serverFlags
	context string
}

func newAdminCmd(g *globals) *cobra.Command {
	f := &adminFlags{g: g}
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Instance administration, on the server host or through a context with an instance admin key",
		Long: `Instance administration. vink admin runs in one of two places:

  on the server host, against the database file: with --db or --config,
  VINK_DB_PATH or VINK_CONFIG_FILE, or when ./vink.db is here;
  anywhere else, through /api/v1/admin: with --context, or the current
  context, whose key must be an instance admin key (vka_…).

Make an instance admin key in the web UI's Instance admin › API keys, or
with vink admin key create on the server host. init, backup and key
create only run on the server host. -d says which place was picked.`,
	}
	f.server.add(cmd)
	cmd.PersistentFlags().StringVar(&f.context, "context", "", "run through this context's server; its key must be an instance admin key (vka_…)")
	cmd.AddCommand(newAdminInitCmd(f), newAdminOrgCmd(f), newAdminUserCmd(f), newAdminAgentCmd(f), newAdminKeyCmd(f), newAdminBackupCmd(f))
	return cmd
}

// adminMode is where vink admin runs, and why there.
type adminMode struct {
	local bool
	why   string
}

// pickAdminMode decides, in order: --db, --config, VINK_DB_PATH or
// VINK_CONFIG_FILE run on the database file (with --context too is a
// mistake); --context runs through it; a ./vink.db here runs on it;
// otherwise the current context, or VINK_SERVER and VINK_KEY.
func pickAdminMode(dbFlag, configFlag, contextFlag string, getenv func(string) string, exists func(string) bool) (adminMode, error) {
	localBy := ""
	switch {
	case dbFlag != "":
		localBy = "--db"
	case configFlag != "":
		localBy = "--config"
	case getenv("VINK_DB_PATH") != "":
		localBy = "VINK_DB_PATH"
	case getenv("VINK_CONFIG_FILE") != "":
		localBy = "VINK_CONFIG_FILE"
	}
	switch {
	case localBy != "" && contextFlag != "":
		return adminMode{}, cli.UserError("%s runs vink admin on the database file and --context runs it through a server; pass one of them", localBy)
	case localBy != "":
		return adminMode{local: true, why: localBy}, nil
	case contextFlag != "":
		return adminMode{why: "--context " + contextFlag}, nil
	case exists("vink.db"):
		return adminMode{local: true, why: "./vink.db is here"}, nil
	}
	return adminMode{why: "no --db, --config or ./vink.db, so the current context"}, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// adminRun is what a command runs against.
type adminRun struct {
	b adminapi.Backend
	// server is the base URL people reach the server at, for the lines
	// printed after creating something.
	server string
}

// run picks the backend and hands it to fn.
func (f *adminFlags) run(cmd *cobra.Command, fn func(r adminRun) error) error {
	mode, err := pickAdminMode(f.server.dbPath, f.server.config, f.context, os.Getenv, fileExists)
	if err != nil {
		return err
	}
	if mode.local {
		return f.server.withService(cmd, func(svc *service.Service, cfg *config.Config) error {
			if f.g.debug {
				fmt.Fprintf(cmd.ErrOrStderr(), "vink admin: on the database file %s (%s)\n", cfg.DB.Path, mode.why)
			}
			return fn(adminRun{b: adminapi.Direct{Svc: svc, Scope: adminScope}, server: strings.TrimRight(cfg.Server.BaseURL, "/")})
		})
	}
	contexts, err := cli.LoadConfig(cli.ConfigPath())
	if err != nil {
		return err
	}
	res, err := contexts.Resolve(f.context, os.Getenv)
	if err != nil {
		return cli.UserError("no database at ./vink.db and no context: on the server host pass --db or --config; elsewhere add a context with an instance admin key, vink ctx add admin --server <url> --key vka_…")
	}
	if !cli.IsAdminKey(res.Key) {
		return cli.UserError("this context's key is not an instance admin key; create one in Instance admin › API keys or with vink admin key create on the server host")
	}
	if f.g.debug {
		name := res.Name
		if res.FromEnv {
			name = "VINK_SERVER and VINK_KEY"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "vink admin: through %s at %s (%s)\n", name, res.Server, mode.why)
	}
	c := cli.NewClient(res.Server, res.Key)
	c.Debug = f.g.debug
	c.Log = cmd.ErrOrStderr()
	return fn(adminRun{b: cli.AdminClient{C: c}, server: res.Server})
}

// localOnly refuses --context for a command that needs the database file.
func (f *adminFlags) localOnly(cmd *cobra.Command) error {
	if f.context != "" {
		return cli.UserError("%s runs on the server host against the database file; pass --db or --config", cmd.CommandPath())
	}
	return nil
}

// printJSON writes v as one line of JSON.
func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// items is the --json shape of a list, as the API sends it.
type items[T any] struct {
	Items []T `json:"items"`
}

func printTable(cmd *cobra.Command, header []string, rows [][]string) error {
	(&cli.Printer{Out: cmd.OutOrStdout()}).Table(header, rows)
	return nil
}

func day(t time.Time) string { return t.Local().Format("2 Jan 2006") }

func lastUsed(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func newAdminBackupCmd(f *adminFlags) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "backup [--out vink-backup.db]",
		Short: "Write a consistent copy of the database with VACUUM INTO (server host only)",
		Long:  "Copies the live database to a new file while the server may keep running: SQLite's VACUUM INTO writes a compact, consistent snapshot. The secret key file is not included; back it up separately.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.localOnly(cmd); err != nil {
				return err
			}
			d, cfg, err := f.server.open(cmd.Context())
			if err != nil {
				return err
			}
			defer d.Close()
			if out == "" {
				out = strings.TrimSuffix(cfg.DB.Path, ".db") + "-backup-" + time.Now().UTC().Format("20060102-150405") + ".db"
			}
			if _, err := os.Stat(out); err == nil {
				return fmt.Errorf("%s exists; pick another --out", out)
			}
			if _, err := d.Writer.ExecContext(cmd.Context(), "VACUUM INTO '"+strings.ReplaceAll(out, "'", "''")+"'"); err != nil { //nolint:gosec // VACUUM INTO takes no bind parameters; the path is quoted with '' escaping
				return fmt.Errorf("backup: %w", err)
			}
			info, err := os.Stat(out)
			if err != nil {
				return err
			}
			// the server page warns when this is older than a day
			if err := db.New(d.Writer).SetInstanceMeta(cmd.Context(), db.SetInstanceMetaParams{Name: service.MetaLastBackup, Value: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UnixMilli()}); err != nil {
				return fmt.Errorf("record the backup: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes)\n", out, info.Size())
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "destination file (default: <db>-backup-<time>.db beside the database)")
	return cmd
}

// withService opens the database and hands a service to fn.
func (f *serverFlags) withService(cmd *cobra.Command, fn func(*service.Service, *config.Config) error) error {
	d, cfg, err := f.open(cmd.Context())
	if err != nil {
		return err
	}
	defer d.Close()
	log := logging.FromContext(cmd.Context())
	if _, err := dbMigrate(cmd, d, log); err != nil {
		return err
	}
	keyring, err := secrets.Load(cfg.SecretKeyFile())
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	svcCfg := service.DefaultConfig()
	svcCfg.PingBaseURL = cfg.PingBaseURL()
	svcCfg.BaseURL = strings.TrimRight(cfg.Server.BaseURL, "/")
	svcCfg.BodyLimit = int64(cfg.Ping.BodyLimit)
	svcCfg.Keyring = keyring
	cmd.SetContext(audit.WithRequest(cmd.Context(), audit.Request{Via: audit.ViaCLI}))
	if err := fn(service.New(d, nil, log, svcCfg), cfg); err != nil {
		// name the database, so a command aimed at the wrong file says so
		return fmt.Errorf("%w (database %s)", err, cfg.DB.Path)
	}
	return nil
}

// readPassword reads one line from stdin when --password-stdin is set.
func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	if !fromStdin {
		return "", errors.New("pass --password-stdin and pipe the password on stdin")
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("empty password on stdin")
	}
	return pw, nil
}

func newAdminInitCmd(f *adminFlags) *cobra.Command {
	var in service.BootstrapInput
	var passwordStdin, asJSON bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap an empty database: instance admin, first org, first project, rw API key (server host only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.localOnly(cmd); err != nil {
				return err
			}
			f.server.createDB = true
			pw, err := readPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}
			in.Password = pw
			return f.server.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				res, err := svc.Bootstrap(cmd.Context(), in)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				base := svc.Config().PingBaseURL
				if asJSON {
					return json.NewEncoder(out).Encode(map[string]any{
						"user": res.User.Subject, "org": res.Org.Slug, "project": res.Project.Slug, "timezone": res.Project.Timezone,
						"ping_key": res.Project.PingKey, "api_key": res.APIKeyPlain, "api_key_prefix": res.APIKey.Prefix,
						"ping_url": base + "/ping/" + res.Project.PingKey + "/<slug>",
					})
				}
				fmt.Fprintf(out, "instance admin  %s\n", res.User.Subject)
				fmt.Fprintf(out, "org             %s\n", res.Org.Slug)
				fmt.Fprintf(out, "project         %s (%s)\n", res.Project.Slug, res.Project.Timezone)
				fmt.Fprintf(out, "ping key        %s\n", res.Project.PingKey)
				fmt.Fprintf(out, "api key (rw)    %s\n", res.APIKeyPlain)
				fmt.Fprintf(out, "\nThe API key is shown once. Ping URLs look like:\n  %s/ping/%s/<slug>\n", base, res.Project.PingKey)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&in.OrgSlug, "org", "", "slug of the first org (required)")
	cmd.Flags().StringVar(&in.OrgName, "org-name", "", "display name of the org (default: the slug)")
	cmd.Flags().StringVar(&in.ProjectSlug, "project", "", "slug of the first project (default: the org slug)")
	cmd.Flags().StringVar(&in.ProjectName, "project-name", "", "display name of the project")
	cmd.Flags().StringVar(&in.Timezone, "timezone", "UTC", "IANA timezone of the project")
	cmd.Flags().StringVar(&in.Subject, "user", "", "login name of the instance admin (required)")
	cmd.Flags().StringVar(&in.Email, "email", "", "email of the instance admin")
	cmd.Flags().StringVar(&in.Name, "name", "", "display name of the instance admin")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	_ = cmd.MarkFlagRequired("org")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}

func newAdminOrgCmd(f *adminFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "org", Short: "Manage orgs"}
	var name, owner string
	create := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create an org",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				org, err := r.b.CreateOrg(cmd.Context(), adminapi.OrgCreate{Slug: args[0], Name: name, Owner: owner})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created org %s (%s)\n", org.Slug, org.ID)
				return nil
			})
		},
	}
	create.Flags().StringVar(&name, "name", "", "display name (default: the slug)")
	create.Flags().StringVar(&owner, "owner", "", "an existing user who becomes the first owner")
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List orgs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd, func(r adminRun) error {
				orgs, err := r.b.ListOrgs(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, items[adminapi.Org]{orgs})
				}
				rows := make([][]string, 0, len(orgs))
				for _, o := range orgs {
					owners := strings.Join(o.Owners, ", ")
					if owners == "" {
						owners = "-"
					}
					rows = append(rows, []string{o.Slug, o.Name, strconv.Itoa(o.Projects), strconv.Itoa(o.Monitors), strconv.Itoa(o.Agents), owners, o.CreatedAt.Format("2006-01-02")})
				}
				return printTable(cmd, []string{"SLUG", "NAME", "PROJECTS", "MONITORS", "AGENTS", "OWNERS", "CREATED"}, rows)
			})
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.AddCommand(create, ls, newAdminOrgKeyCmd(f))
	return cmd
}

// newAdminOrgKeyCmd manages org keys: API keys that export and apply
// every project of an org, for GitOps from a workstation.
func newAdminOrgKeyCmd(f *adminFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage org keys, which export and apply every project of an org"}
	var orgSlug string
	cmd.PersistentFlags().StringVar(&orgSlug, "org", "", "org slug (required)")
	_ = cmd.MarkPersistentFlagRequired("org")
	var name, access string
	var asJSON bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Issue an org key and print it once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd, func(r adminRun) error {
				k, err := r.b.CreateOrgKey(cmd.Context(), orgSlug, adminapi.KeyCreate{Name: name, Access: domain.Access(access)})
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, k)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created org key %s (%s, %s)\ntoken (shown once)  %s\n\nvink ctx add %s-org --server %s --key %s\nvink export --org %s -o %s.yaml\n", k.Name, k.Prefix, k.Access, k.Key, orgSlug, r.server, k.Key, orgSlug, orgSlug)
				return nil
			})
		},
	}
	create.Flags().StringVar(&name, "name", "", "a name for the key (default: org key)")
	create.Flags().StringVar(&access, "access", "ro", "ro exports; rw exports with secrets and applies")
	create.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	var lsJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the org's org keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd, func(r adminRun) error {
				keys, err := r.b.ListOrgKeys(cmd.Context(), orgSlug)
				if err != nil {
					return err
				}
				if lsJSON {
					return printJSON(cmd, items[adminapi.OrgKey]{keys})
				}
				rows := make([][]string, 0, len(keys))
				for _, k := range keys {
					rows = append(rows, []string{k.ID, k.Name, "vk_" + k.Prefix + "…", string(k.Access), k.CreatedAt.Format("2006-01-02"), lastUsed(k.LastUsedAt)})
				}
				return printTable(cmd, []string{"ID", "NAME", "PREFIX", "ACCESS", "CREATED", "LAST USED"}, rows)
			})
		},
	}
	ls.Flags().BoolVar(&lsJSON, "json", false, "print as JSON")
	revoke := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an org key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				if err := r.b.RevokeOrgKey(cmd.Context(), orgSlug, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked org key %s\n", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(create, ls, revoke)
	return cmd
}

func newAdminUserCmd(f *adminFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage users"}
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List users with their roles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd, func(r adminRun) error {
				users, err := r.b.ListUsers(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, items[adminapi.User]{users})
				}
				rows := make([][]string, 0, len(users))
				for _, u := range users {
					roles := make([]string, 0, len(u.Roles))
					for _, m := range u.Roles {
						roles = append(roles, m.Org+":"+string(m.Role))
					}
					status := "active"
					if u.Disabled {
						status = "disabled"
					}
					email := u.Email
					if email == "" {
						email = "-"
					}
					rows = append(rows, []string{u.Subject, email, strconv.FormatBool(u.InstanceAdmin), u.Source, status, strings.Join(roles, ", ")})
				}
				return printTable(cmd, []string{"USER", "EMAIL", "ADMIN", "LOGIN", "STATUS", "ROLES"}, rows)
			})
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")

	setAdmin := func(use, short, done string, admin bool) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <user>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return f.run(cmd, func(r adminRun) error {
					if _, err := r.b.UpdateUser(cmd.Context(), args[0], adminapi.UserPatch{InstanceAdmin: &admin}); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", args[0], done)
					return nil
				})
			},
		}
	}
	promote := setAdmin("promote", "Make a user an instance admin", "is now an instance admin", true)
	demote := setAdmin("demote", "Take instance admin from a user; the admin keys they made are revoked", "is no longer an instance admin", false)

	var email, name, orgSlug, role, source string
	var passwordStdin bool
	create := &cobra.Command{
		Use:   "create <user>",
		Short: "Create an account, optionally with a role in an org",
		Long: `Create an account. --source local (the default) makes one that signs in
with a password, read from stdin. --source proxy or oidc makes one for a
person who signs in through the proxy or the identity provider, before
their first visit, so roles can be given ahead; the name is normalised as
that provider's settings say (strip_realm, lowercase).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := adminapi.UserCreate{Subject: args[0], Source: source, Email: email, Name: name}
			switch source {
			case "local":
				pw, err := readPassword(cmd, passwordStdin)
				if err != nil {
					return err
				}
				in.Password = pw
			case "proxy", "oidc":
				if passwordStdin {
					return cli.UserError("only local accounts have a password; drop --password-stdin")
				}
			default:
				return cli.UserError("--source must be local, proxy or oidc")
			}
			return f.run(cmd, func(r adminRun) error {
				ctx := cmd.Context()
				u, err := r.b.CreateUser(ctx, in)
				if err != nil {
					return err
				}
				if orgSlug != "" {
					if _, err := r.b.Grant(ctx, u.Subject, orgSlug, domain.Role(role)); err != nil {
						return fmt.Errorf("created user %s, but the role in %s: %w", u.Subject, orgSlug, err)
					}
				}
				if source == "local" {
					fmt.Fprintf(cmd.OutOrStdout(), "created user %s\n", u.Subject)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "created user %s, who signs in through %s\n", u.Subject, source)
				}
				return nil
			})
		},
	}
	create.Flags().StringVar(&source, "source", "local", "local (a password), proxy or oidc")
	create.Flags().StringVar(&email, "email", "", "email address")
	create.Flags().StringVar(&name, "name", "", "display name")
	create.Flags().StringVar(&orgSlug, "org", "", "org to add the user to")
	create.Flags().StringVar(&role, "role", "member", "role in that org: owner, admin, member or viewer")
	create.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin (local accounts)")

	var grantOrg, grantRole string
	grant := &cobra.Command{
		Use:   "grant <user> --org <slug> --role <role>",
		Short: "Give an existing user a role in an org, or change it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				u, err := r.b.Grant(cmd.Context(), args[0], grantOrg, domain.Role(grantRole))
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is now %s in %s\n", u.Subject, grantRole, grantOrg)
				return nil
			})
		},
	}
	grant.Flags().StringVar(&grantOrg, "org", "", "org slug (required)")
	grant.Flags().StringVar(&grantRole, "role", "member", "owner, admin, member or viewer")
	_ = grant.MarkFlagRequired("org")

	var revokeOrg string
	revoke := &cobra.Command{
		Use:   "revoke <user> --org <slug>",
		Short: "Remove a user from an org; the last owner stays",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				if err := r.b.Ungrant(cmd.Context(), args[0], revokeOrg); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is no longer in %s\n", args[0], revokeOrg)
				return nil
			})
		},
	}
	revoke.Flags().StringVar(&revokeOrg, "org", "", "org slug (required)")
	_ = revoke.MarkFlagRequired("org")
	totpReset := &cobra.Command{
		Use:   "totp-reset <user>",
		Short: "Turn two-factor off for a user who lost the phone; they set it up again at the next sign-in",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				if err := r.b.ResetTOTP(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "two-factor is off for %s\n", args[0])
				return nil
			})
		},
	}

	resetLink := &cobra.Command{
		Use:   "reset-link <user>",
		Short: "Print a one-time password reset link for a local account, valid for a day",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				link, err := r.b.CreateResetLink(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\nworks once, until %s\n", link.URL, link.ExpiresAt.UTC().Format(time.RFC3339))
				return nil
			})
		},
	}

	cmd.AddCommand(ls, promote, demote, create, grant, revoke, totpReset, resetLink)
	return cmd
}

// newAdminAgentCmd manages an org's probe agents.
func newAdminAgentCmd(f *adminFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Manage an org's probe agents"}
	var orgSlug string
	cmd.PersistentFlags().StringVar(&orgSlug, "org", "", "org slug (required)")
	_ = cmd.MarkPersistentFlagRequired("org")

	var labels string
	var asJSON bool
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Register an agent and print its token once, with the command to run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := domain.ParseLabels(labels)
			if err != nil {
				return err
			}
			return f.run(cmd, func(r adminRun) error {
				a, err := r.b.CreateAgent(cmd.Context(), orgSlug, adminapi.AgentCreate{Name: args[0], Labels: parsed})
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, a)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created agent %s\ntoken (shown once)  %s\n\nrun on the agent's host:\n  %s\n", a.Name, a.Token, strings.ReplaceAll(a.Command, "\n", "\n  "))
				return nil
			})
		},
	}
	add.Flags().StringVar(&labels, "labels", "", "labels monitors can select, like site=dc2,zone=dmz")
	add.Flags().BoolVar(&asJSON, "json", false, "print as JSON")

	var lsJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the org's agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd, func(r adminRun) error {
				agents, err := r.b.ListAgents(cmd.Context(), orgSlug)
				if err != nil {
					return err
				}
				if lsJSON {
					return printJSON(cmd, items[adminapi.Agent]{agents})
				}
				rows := make([][]string, 0, len(agents))
				for _, a := range agents {
					version := a.Version
					if version == "" {
						version = "-"
					}
					rows = append(rows, []string{a.Name, domain.LabelsString(a.Labels), lastUsed(a.LastSeenAt), version, "vat_" + a.TokenPrefix + "_…"})
				}
				return printTable(cmd, []string{"NAME", "LABELS", "LAST SEEN", "VERSION", "TOKEN"}, rows)
			})
		},
	}
	ls.Flags().BoolVar(&lsJSON, "json", false, "print as JSON")

	revoke := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke an agent's token and forget it; its monitors turn late",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				if err := r.b.RevokeAgent(cmd.Context(), orgSlug, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked agent %s\n", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(add, ls, revoke)
	return cmd
}

// newAdminKeyCmd manages instance admin keys: vka_ keys that run vink
// admin through a context. They are made only here, on the server host,
// or in the web UI.
func newAdminKeyCmd(f *adminFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage instance admin keys (vka_…), which run vink admin through a context"}
	var name, access, expires string
	var asJSON bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Make an instance admin key and print it once (server host only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.localOnly(cmd); err != nil {
				return err
			}
			ttl, err := parseExpires(expires)
			if err != nil {
				return err
			}
			return f.server.withService(cmd, func(svc *service.Service, cfg *config.Config) error {
				k, err := adminapi.Direct{Svc: svc, Scope: adminScope}.CreateAdminKey(cmd.Context(), adminapi.AdminKeyCreate{Name: name, Access: domain.Access(access), TTL: ttl})
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, k)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created admin key %s (vka_%s…, %s, expires %s)\ntoken (shown once)  %s\n\nvink ctx add admin --server %s --key %s\n",
					k.Name, k.Prefix, k.Access, day(k.ExpiresAt), k.Key, strings.TrimRight(cfg.Server.BaseURL, "/"), k.Key)
				return nil
			})
		},
	}
	create.Flags().StringVar(&name, "name", "", "a name for the key, like the machine it lives on (default: admin key)")
	create.Flags().StringVar(&access, "access", "ro", "ro reads; rw also changes")
	create.Flags().StringVar(&expires, "expires", "90d", "lifetime, between 1d and 365d")
	create.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	var lsJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List instance admin keys, expired ones included",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd, func(r adminRun) error {
				keys, err := r.b.ListAdminKeys(cmd.Context())
				if err != nil {
					return err
				}
				if lsJSON {
					return printJSON(cmd, items[adminapi.AdminKey]{keys})
				}
				rows := make([][]string, 0, len(keys))
				for _, k := range keys {
					by := k.CreatedBy
					if by == "" {
						by = "server host"
					}
					exp := "expires " + day(k.ExpiresAt)
					if k.Expired {
						exp = "expired " + day(k.ExpiresAt)
					}
					rows = append(rows, []string{k.ID, k.Name, "vka_" + k.Prefix + "…", string(k.Access), by, exp, lastUsed(k.LastUsedAt)})
				}
				return printTable(cmd, []string{"ID", "NAME", "PREFIX", "ACCESS", "CREATED BY", "EXPIRY", "LAST USED"}, rows)
			})
		},
	}
	ls.Flags().BoolVar(&lsJSON, "json", false, "print as JSON")
	revoke := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an instance admin key; it stops working at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.run(cmd, func(r adminRun) error {
				if err := r.b.RevokeAdminKey(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked admin key %s\n", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(create, ls, revoke)
	return cmd
}

// parseExpires reads 30d, 90d or 365d; a bare number is days.
func parseExpires(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		s = strconv.Itoa(n) + "d"
	}
	d, err := domain.ParseDuration(s)
	if err != nil {
		return 0, cli.UserError("--expires: %v; use days, like 30d, 90d or 365d", err)
	}
	return d.Std(), nil
}
