package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/audit"
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

func newAdminCmd() *cobra.Command {
	f := &serverFlags{}
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Instance administration on the server host (no network)",
	}
	f.add(cmd)
	cmd.AddCommand(newAdminInitCmd(f), newAdminOrgCmd(f), newAdminUserCmd(f), newAdminAgentCmd(f), newAdminBackupCmd(f))
	return cmd
}

func newAdminBackupCmd(f *serverFlags) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "backup [--out vink-backup.db]",
		Short: "Write a consistent copy of the database with VACUUM INTO",
		Long:  "Copies the live database to a new file while the server may keep running: SQLite's VACUUM INTO writes a compact, consistent snapshot. The secret key file is not included; back it up separately.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, cfg, err := f.open(cmd.Context())
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

func newAdminInitCmd(f *serverFlags) *cobra.Command {
	var in service.BootstrapInput
	var passwordStdin, asJSON bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap an empty database: instance admin, first org, first project, rw API key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f.createDB = true
			pw, err := readPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}
			in.Password = pw
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
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

func newAdminOrgCmd(f *serverFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "org", Short: "Manage orgs"}
	var name string
	create := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create an org",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				org, err := svc.CreateOrg(cmd.Context(), adminScope, args[0], name)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created org %s (%s)\n", org.Slug, org.ID)
				return nil
			})
		},
	}
	create.Flags().StringVar(&name, "name", "", "display name (default: the slug)")
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List orgs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				orgs, err := svc.ListOrgs(cmd.Context(), adminScope)
				if err != nil {
					return err
				}
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(orgs)
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "SLUG\tNAME\tCREATED")
				for _, o := range orgs {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", o.Slug, o.Name, o.CreatedAt.Format("2006-01-02"))
				}
				return tw.Flush()
			})
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.AddCommand(create, ls, newAdminOrgKeyCmd(f))
	return cmd
}

// newAdminOrgKeyCmd manages org keys: API keys that export and apply
// every project of an org, for GitOps from a workstation.
func newAdminOrgKeyCmd(f *serverFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage org keys, which export and apply every project of an org"}
	var orgSlug string
	cmd.PersistentFlags().StringVar(&orgSlug, "org", "", "org slug (required)")
	_ = cmd.MarkPersistentFlagRequired("org")
	orgScope := func(cmd *cobra.Command, svc *service.Service) (domain.Scope, error) {
		org, err := svc.OrgBySlug(cmd.Context(), orgSlug)
		if err != nil {
			return domain.Scope{}, err
		}
		sc := adminScope
		sc.OrgID = org.ID
		return sc, nil
	}
	var name, access string
	var asJSON bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Issue an org key and print it once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.withService(cmd, func(svc *service.Service, cfg *config.Config) error {
				sc, err := orgScope(cmd, svc)
				if err != nil {
					return err
				}
				k, token, err := svc.CreateOrgAPIKey(cmd.Context(), sc, name, domain.Access(access))
				if err != nil {
					return err
				}
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"id": k.ID, "name": k.Name, "prefix": k.Prefix, "access": k.Access, "token": token})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created org key %s (%s, %s)\ntoken (shown once)  %s\n\nvink ctx add %s-org --server %s --key %s\nvink export --org %s -o %s.yaml\n", k.Name, k.Prefix, k.Access, token, orgSlug, strings.TrimRight(cfg.Server.BaseURL, "/"), token, orgSlug, orgSlug)
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
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				sc, err := orgScope(cmd, svc)
				if err != nil {
					return err
				}
				keys, err := svc.ListOrgAPIKeys(cmd.Context(), sc)
				if err != nil {
					return err
				}
				if lsJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(keys)
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tACCESS\tCREATED\tLAST USED")
				for _, k := range keys {
					used := "never"
					if k.LastUsedAt != nil {
						used = k.LastUsedAt.Local().Format("2006-01-02 15:04")
					}
					fmt.Fprintf(tw, "%s\t%s\tvk_%s…\t%s\t%s\t%s\n", k.ID, k.Name, k.Prefix, k.Access, k.CreatedAt.Format("2006-01-02"), used)
				}
				return tw.Flush()
			})
		},
	}
	ls.Flags().BoolVar(&lsJSON, "json", false, "print as JSON")
	revoke := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an org key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				sc, err := orgScope(cmd, svc)
				if err != nil {
					return err
				}
				if err := svc.RevokeOrgAPIKey(cmd.Context(), sc, args[0]); err != nil {
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

func newAdminUserCmd(f *serverFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage users"}
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				users, err := svc.ListUsers(cmd.Context(), adminScope)
				if err != nil {
					return err
				}
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(users)
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "USER\tEMAIL\tADMIN\tLOGIN")
				for _, u := range users {
					login := "proxy"
					if u.HasPassword {
						login = "local"
					}
					fmt.Fprintf(tw, "%s\t%s\t%v\t%s\n", u.Subject, u.Email, u.InstanceAdmin, login)
				}
				return tw.Flush()
			})
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")

	promote := &cobra.Command{
		Use:   "promote <user>",
		Short: "Make a user an instance admin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				if err := svc.SetInstanceAdmin(cmd.Context(), adminScope, args[0], true); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is now an instance admin\n", args[0])
				return nil
			})
		},
	}

	var email, name, orgSlug, role string
	var passwordStdin bool
	create := &cobra.Command{
		Use:   "create <user>",
		Short: "Create a local user, optionally with a role in an org",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pw, err := readPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				ctx := cmd.Context()
				u, err := svc.CreateLocalUser(ctx, adminScope, args[0], email, name, pw, false)
				if err != nil {
					return err
				}
				if orgSlug != "" {
					org, err := svc.OrgBySlug(ctx, orgSlug)
					if err != nil {
						return err
					}
					if err := svc.SetMembership(ctx, adminScope, u.ID, org.ID, domain.Role(role)); err != nil {
						return err
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created user %s\n", u.Subject)
				return nil
			})
		},
	}
	create.Flags().StringVar(&email, "email", "", "email address")
	create.Flags().StringVar(&name, "name", "", "display name")
	create.Flags().StringVar(&orgSlug, "org", "", "org to add the user to")
	create.Flags().StringVar(&role, "role", "member", "role in that org: owner, admin, member or viewer")
	create.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")

	var grantOrg, grantRole string
	grant := &cobra.Command{
		Use:   "grant <user> --org <slug> --role <role>",
		Short: "Give an existing user a role in an org, or change it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				ctx := cmd.Context()
				u, err := svc.UserBySubject(ctx, args[0])
				if err != nil {
					return err
				}
				org, err := svc.OrgBySlug(ctx, grantOrg)
				if err != nil {
					return err
				}
				if err := svc.SetMembership(ctx, adminScope, u.ID, org.ID, domain.Role(grantRole)); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is now %s in %s\n", u.Subject, grantRole, org.Slug)
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
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				ctx := cmd.Context()
				u, err := svc.UserBySubject(ctx, args[0])
				if err != nil {
					return err
				}
				org, err := svc.OrgBySlug(ctx, revokeOrg)
				if err != nil {
					return err
				}
				if err := svc.RemoveMembership(ctx, adminScope, u.ID, org.ID); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is no longer in %s\n", u.Subject, org.Slug)
				return nil
			})
		},
	}
	revoke.Flags().StringVar(&revokeOrg, "org", "", "org slug (required)")
	_ = revoke.MarkFlagRequired("org")
	cmd.AddCommand(ls, promote, create, grant, revoke)
	return cmd
}

// newAdminAgentCmd manages an org's probe agents from the server host.
func newAdminAgentCmd(f *serverFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Manage an org's probe agents"}
	var orgSlug string
	cmd.PersistentFlags().StringVar(&orgSlug, "org", "", "org slug (required)")
	_ = cmd.MarkPersistentFlagRequired("org")
	orgScope := func(cmd *cobra.Command, svc *service.Service) (domain.Scope, error) {
		org, err := svc.OrgBySlug(cmd.Context(), orgSlug)
		if err != nil {
			return domain.Scope{}, err
		}
		sc := adminScope
		sc.OrgID = org.ID
		return sc, nil
	}

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
			return f.withService(cmd, func(svc *service.Service, cfg *config.Config) error {
				sc, err := orgScope(cmd, svc)
				if err != nil {
					return err
				}
				a, token, err := svc.CreateAgent(cmd.Context(), sc, args[0], parsed)
				if err != nil {
					return err
				}
				command := domain.AgentCommand(cfg.Server.BaseURL, token, a.Labels)
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
						"name": a.Name, "id": a.ID, "labels": a.Labels, "token": token, "command": command,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created agent %s\ntoken (shown once)  %s\n\nrun on the agent's host:\n  %s\n", a.Name, token, strings.ReplaceAll(command, "\n", "\n  "))
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
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				sc, err := orgScope(cmd, svc)
				if err != nil {
					return err
				}
				agents, err := svc.ListAgents(cmd.Context(), sc)
				if err != nil {
					return err
				}
				if lsJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(agents)
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tLABELS\tLAST SEEN\tVERSION\tTOKEN")
				for _, a := range agents {
					seen := "never"
					if a.LastSeenAt != nil {
						seen = a.LastSeenAt.Local().Format("2006-01-02 15:04")
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\tvat_%s_…\n", a.Name, domain.LabelsString(a.Labels), seen, a.Version, a.TokenPrefix)
				}
				return tw.Flush()
			})
		},
	}
	ls.Flags().BoolVar(&lsJSON, "json", false, "print as JSON")

	revoke := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke an agent's token and forget it; its monitors turn late",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withService(cmd, func(svc *service.Service, _ *config.Config) error {
				sc, err := orgScope(cmd, svc)
				if err != nil {
					return err
				}
				if err := svc.RevokeAgent(cmd.Context(), sc, args[0]); err != nil {
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
