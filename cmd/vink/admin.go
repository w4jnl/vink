package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

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
	cmd.AddCommand(newAdminInitCmd(f), newAdminOrgCmd(f), newAdminUserCmd(f))
	return cmd
}

// withService opens the database and hands a service to fn.
func (f *serverFlags) withService(cmd *cobra.Command, fn func(*service.Service) error) error {
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
	return fn(service.New(d, nil, log, svcCfg))
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
			pw, err := readPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}
			in.Password = pw
			return f.withService(cmd, func(svc *service.Service) error {
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
			return f.withService(cmd, func(svc *service.Service) error {
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
			return f.withService(cmd, func(svc *service.Service) error {
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
	cmd.AddCommand(create, ls)
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
			return f.withService(cmd, func(svc *service.Service) error {
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
			return f.withService(cmd, func(svc *service.Service) error {
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
			return f.withService(cmd, func(svc *service.Service) error {
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
	cmd.AddCommand(ls, promote, create)
	return cmd
}
