package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/api"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return usageError()
	}
	if os.Args[1] == "version" {
		fmt.Println(config.VersionFromEnv())
		return nil
	}
	if err := config.LoadDotenv(); err != nil {
		return fmt.Errorf("load dotenv: %w", err)
	}
	cfg := config.FromEnv()
	database, err := paondb.Open(cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := paondb.SchemaAvailable(database); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}
	operations := api.NewOperations(cfg, database)
	defer operations.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch os.Args[1] {
	case "accounts":
		return runAccounts(ctx, operations, os.Args[2:])
	case "settings":
		return runSettings(ctx, operations, os.Args[2:])
	case "domains":
		return runDomains(ctx, operations, os.Args[2:])
	case "email-domain-blocks":
		return runEmailDomainBlocks(ctx, operations, os.Args[2:])
	case "canonical-email-blocks":
		return runCanonicalEmailBlocks(ctx, operations, os.Args[2:])
	case "ip-blocks":
		return runIPBlocks(ctx, operations, os.Args[2:])
	case "feeds":
		return runFeeds(ctx, operations, os.Args[2:])
	case "cache":
		return runCache(ctx, operations, os.Args[2:])
	case "vacuum":
		return runVacuum(ctx, operations, os.Args[2:])
	default:
		return usageError()
	}
}

func runAccounts(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: paon-admin accounts <create|modify|delete|rotate> ...")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("accounts create", flag.ContinueOnError)
		email := flags.String("email", "", "account email address")
		password := flags.String("password", "", "initial password; generated when omitted")
		role := flags.String("role", "", "role name")
		confirmed := flags.Bool("confirmed", false, "mark email confirmed")
		approved := flags.Bool("approved", false, "approve the account")
		if err := flags.Parse(commandFlagArgs(args[1:])); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: paon-admin accounts create USERNAME --email ADDRESS [--password PASSWORD] [--confirmed] [--approved] [--role NAME]")
		}
		generated := false
		if *password == "" {
			*password = randomPassword()
			generated = true
		}
		user, err := operations.CreateAccount(ctx, api.OperationAccountCreate{Username: flags.Arg(0), Email: *email, Password: *password, Role: *role, Confirmed: *confirmed, Approved: *approved})
		if err != nil {
			return err
		}
		fmt.Printf("created @%s user_id=%d account_id=%d\n", user.Account.Username, user.ID, user.AccountID)
		if generated {
			fmt.Printf("password=%s\n", *password)
		}
		return nil
	case "modify":
		flags := flag.NewFlagSet("accounts modify", flag.ContinueOnError)
		email := flags.String("email", "", "replace email")
		role := flags.String("role", "", "replace role")
		removeRole := flags.Bool("remove-role", false, "remove assigned role")
		confirm := flags.Bool("confirm", false, "confirm email")
		approve := flags.Bool("approve", false, "approve account")
		enable := flags.Bool("enable", false, "enable login")
		disable := flags.Bool("disable", false, "disable login")
		disable2FA := flags.Bool("disable-2fa", false, "disable two-factor authentication")
		resetPassword := flags.Bool("reset-password", false, "generate and set a new password")
		if err := flags.Parse(commandFlagArgs(args[1:])); err != nil {
			return err
		}
		if flags.NArg() != 1 || (*enable && *disable) || (*removeRole && *role != "") {
			return errors.New("usage: paon-admin accounts modify USERNAME [--email ADDRESS] [--role NAME|--remove-role] [--confirm] [--approve] [--enable|--disable] [--disable-2fa] [--reset-password]")
		}
		result, err := operations.ModifyAccount(ctx, flags.Arg(0), api.OperationAccountModify{Email: *email, Role: *role, RemoveRole: *removeRole, Confirm: *confirm, Approve: *approve, Enable: *enable, Disable: *disable, Disable2FA: *disable2FA, ResetPassword: *resetPassword})
		if err != nil {
			return err
		}
		fmt.Printf("modified @%s user_id=%d\n", result.User.Account.Username, result.User.ID)
		if result.GeneratedPassword != "" {
			fmt.Printf("password=%s\n", result.GeneratedPassword)
		}
		return nil
	case "delete":
		flags := flag.NewFlagSet("accounts delete", flag.ContinueOnError)
		confirm := flags.Bool("confirm", false, "queue destructive deletion")
		dryRun := flags.Bool("dry-run", false, "show the target without queueing")
		if err := flags.Parse(commandFlagArgs(args[1:])); err != nil {
			return err
		}
		if flags.NArg() != 1 || (!*confirm && !*dryRun) {
			return errors.New("accounts delete requires USERNAME and either --dry-run or --confirm")
		}
		user, err := operations.LocalAccountSummary(ctx, flags.Arg(0))
		if err != nil {
			return err
		}
		if *dryRun {
			fmt.Printf("would queue deletion for @%s user_id=%d account_id=%d\n", user.Account.Username, user.ID, user.AccountID)
			return nil
		}
		if err := operations.QueueAccountDeletion(ctx, flags.Arg(0)); err != nil {
			return err
		}
		fmt.Printf("queued deletion for @%s account_id=%d\n", user.Account.Username, user.AccountID)
		return nil
	case "rotate":
		flags := flag.NewFlagSet("accounts rotate", flag.ContinueOnError)
		confirm := flags.Bool("confirm", false, "rotate and federate the new key")
		all := flags.Bool("all", false, "rotate every nonsuspended local account")
		if err := flags.Parse(commandFlagArgs(args[1:])); err != nil {
			return err
		}
		if !*confirm || (*all && flags.NArg() != 0) || (!*all && flags.NArg() != 1) {
			return errors.New("accounts rotate requires USERNAME or --all, plus --confirm")
		}
		if *all {
			count, err := operations.RotateAllAccountKeys(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("rotated=%d\n", count)
			return nil
		}
		if err := operations.RotateAccountKey(ctx, flags.Arg(0)); err != nil {
			return err
		}
		fmt.Printf("rotated key for @%s\n", flags.Arg(0))
		return nil
	case "cull":
		flags := flag.NewFlagSet("accounts cull", flag.ContinueOnError)
		concurrency := flags.Int("concurrency", 5, "parallel remote actor checks")
		dryRun := flags.Bool("dry-run", false, "count removals without deleting accounts")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		result, err := operations.CullRemoteAccounts(ctx, flags.Args(), *concurrency, *dryRun)
		if err != nil {
			return err
		}
		fmt.Printf("visited=%d removed=%d dry_run=%t\n", result.Visited, result.Removed, *dryRun)
		for _, domain := range result.UnavailableDomains {
			fmt.Printf("unavailable=%s\n", domain)
		}
		return nil
	default:
		return errors.New("usage: paon-admin accounts <create|modify|delete|rotate|cull> ...")
	}
}

func runSettings(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) < 2 || args[0] != "registrations" {
		return errors.New("usage: paon-admin settings registrations <open|approved|close> [--require-reason]")
	}
	mode := args[1]
	var requireReason *bool
	if mode == "approved" {
		flags := flag.NewFlagSet("settings registrations approved", flag.ContinueOnError)
		required := flags.Bool("require-reason", false, "require an application reason")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		requireReason = required
	}
	if mode == "close" {
		mode = "none"
	}
	if err := operations.SetRegistrationsMode(ctx, mode, requireReason); err != nil {
		return err
	}
	fmt.Println("OK")
	return nil
}

func runDomains(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: paon-admin domains <purge|crawl> ...")
	}
	if args[0] == "crawl" {
		flags := flag.NewFlagSet("domains crawl", flag.ContinueOnError)
		concurrency := flags.Int("concurrency", 50, "parallel domain requests")
		format := flags.String("format", "summary", "summary, domains, or json")
		excludeSuspended := flags.Bool("exclude-suspended", false, "exclude suspended domains and subdomains")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() > 1 || (*format != "summary" && *format != "domains" && *format != "json") {
			return errors.New("domains crawl accepts at most one START and --format summary|domains|json")
		}
		start := ""
		if flags.NArg() == 1 {
			start = flags.Arg(0)
		}
		result, err := operations.CrawlDomains(ctx, start, *concurrency, *excludeSuspended)
		if err != nil {
			return err
		}
		switch *format {
		case "domains":
			domains := make([]string, 0, len(result.Stats))
			for domain := range result.Stats {
				domains = append(domains, domain)
			}
			sort.Strings(domains)
			for _, domain := range domains {
				fmt.Println(domain)
			}
		case "json":
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result.Stats)
		default:
			fmt.Printf("visited=%d failed=%d servers=%d elapsed=%s\n", result.Visited, result.Failed, len(result.Stats), time.Since(result.StartedAt).Round(time.Second))
		}
		return nil
	}
	if args[0] != "purge" {
		return errors.New("usage: paon-admin domains <purge|crawl> ...")
	}
	flags := flag.NewFlagSet("domains purge", flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "count affected accounts")
	confirm := flags.Bool("confirm", false, "queue destructive domain purge")
	if err := flags.Parse(commandFlagArgs(args[1:])); err != nil {
		return err
	}
	if flags.NArg() != 1 || (!*dryRun && !*confirm) {
		return errors.New("domains purge requires DOMAIN and either --dry-run or --confirm")
	}
	domain := strings.ToLower(strings.TrimSpace(flags.Arg(0)))
	count, err := operations.DomainAccountCount(ctx, domain)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("would purge domain=%s accounts=%d\n", domain, count)
		return nil
	}
	if err := operations.QueueDomainPurge(ctx, domain); err != nil {
		return err
	}
	fmt.Printf("queued domain purge domain=%s accounts=%d\n", domain, count)
	return nil
}

func runVacuum(ctx context.Context, operations *api.Operations, args []string) error {
	flags := flag.NewFlagSet("vacuum", flag.ContinueOnError)
	confirm := flags.Bool("confirm", false, "run the destructive vacuum")
	if err := flags.Parse(commandFlagArgs(args)); err != nil {
		return err
	}
	if flags.NArg() != 1 || !*confirm {
		return errors.New("usage: paon-admin vacuum <statuses|media|preview-cards|feeds> --confirm")
	}
	result, err := operations.Vacuum(ctx, flags.Arg(0), time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Printf("statuses=%d media=%d preview_cards=%d feeds=%d\n", result.Statuses, result.Media, result.PreviewCards, result.Feeds)
	return nil
}

func runEmailDomainBlocks(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: paon-admin email-domain-blocks <list|add|remove> [DOMAIN...]")
	}
	switch args[0] {
	case "list":
		domains, err := operations.ListEmailDomainBlocks(ctx)
		if err != nil {
			return err
		}
		for _, domain := range domains {
			fmt.Println(domain)
		}
		return nil
	case "add":
		if len(args) < 2 {
			return errors.New("email-domain-blocks add requires at least one domain")
		}
		added, err := operations.AddEmailDomainBlocks(ctx, args[1:])
		if err != nil {
			return err
		}
		fmt.Printf("added=%d\n", added)
		return nil
	case "remove":
		if len(args) < 2 {
			return errors.New("email-domain-blocks remove requires at least one domain")
		}
		removed, err := operations.RemoveEmailDomainBlocks(ctx, args[1:])
		if err != nil {
			return err
		}
		fmt.Printf("removed=%d\n", removed)
		return nil
	default:
		return errors.New("usage: paon-admin email-domain-blocks <list|add|remove> [DOMAIN...]")
	}
}

func runCanonicalEmailBlocks(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) != 2 || (args[0] != "find" && args[0] != "remove") {
		return errors.New("usage: paon-admin canonical-email-blocks <find|remove> EMAIL")
	}
	if args[0] == "find" {
		found, err := operations.CanonicalEmailBlockExists(ctx, args[1])
		if err != nil {
			return err
		}
		if !found {
			return errors.New("canonical email block not found")
		}
		fmt.Println("found")
		return nil
	}
	removed, err := operations.RemoveCanonicalEmailBlock(ctx, args[1])
	if err != nil {
		return err
	}
	if !removed {
		return errors.New("canonical email block not found")
	}
	fmt.Println("removed")
	return nil
}

func runIPBlocks(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: paon-admin ip-blocks <add|remove|export> ...")
	}
	switch args[0] {
	case "add":
		flags := flag.NewFlagSet("ip-blocks add", flag.ContinueOnError)
		severity := flags.String("severity", "", "sign_up_requires_approval, sign_up_block, or no_access")
		comment := flags.String("comment", "", "operator comment")
		duration := flags.Duration("duration", 0, "optional block duration, for example 24h")
		force := flags.Bool("force", false, "overwrite an existing exact CIDR")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() == 0 || *severity == "" {
			return errors.New("ip-blocks add requires --severity and one or more IP/CIDR values")
		}
		blocks := make([]api.OperationIPBlock, 0, flags.NArg())
		for _, cidr := range flags.Args() {
			block := api.OperationIPBlock{CIDR: cidr, Severity: *severity, Comment: *comment}
			if *duration > 0 {
				block.ExpiresAt.Valid = true
				block.ExpiresAt.Time = time.Now().UTC().Add(*duration)
			}
			blocks = append(blocks, block)
		}
		added, err := operations.AddIPBlocks(ctx, blocks, *force)
		if err != nil {
			return err
		}
		fmt.Printf("changed=%d\n", added)
		return nil
	case "remove":
		if len(args) < 2 {
			return errors.New("ip-blocks remove requires one or more IP/CIDR values")
		}
		removed, err := operations.RemoveIPBlocks(ctx, args[1:])
		if err != nil {
			return err
		}
		fmt.Printf("removed=%d\n", removed)
		return nil
	case "export":
		rows, err := operations.ListIPBlocks(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			fmt.Printf("%s\t%d\t%s\n", row.IP, row.Severity, strings.ReplaceAll(row.Comment, "\t", " "))
		}
		return nil
	default:
		return errors.New("usage: paon-admin ip-blocks <add|remove|export> ...")
	}
}

func runFeeds(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: paon-admin feeds <build|clear|vacuum> ...")
	}
	switch args[0] {
	case "build":
		flags := flag.NewFlagSet("feeds build", flag.ContinueOnError)
		all := flags.Bool("all", false, "build feeds for every local user")
		if err := flags.Parse(commandFlagArgs(args[1:])); err != nil {
			return err
		}
		if (*all && flags.NArg() != 0) || (!*all && flags.NArg() != 1) {
			return errors.New("feeds build requires USERNAME or --all")
		}
		username := ""
		if flags.NArg() == 1 {
			username = flags.Arg(0)
		}
		built, err := operations.BuildHomeFeeds(ctx, username, *all)
		if err != nil {
			return err
		}
		fmt.Printf("built=%d\n", built)
		return nil
	case "clear":
		flags := flag.NewFlagSet("feeds clear", flag.ContinueOnError)
		confirm := flags.Bool("confirm", false, "clear all home/list feed caches")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !*confirm {
			return errors.New("feeds clear requires --confirm")
		}
		cleared, err := operations.ClearFeeds(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("cleared=%d\n", cleared)
		return nil
	case "vacuum":
		if len(args) != 2 || args[1] != "--confirm" {
			return errors.New("feeds vacuum requires --confirm")
		}
		result, err := operations.Vacuum(ctx, "feeds", time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Printf("feeds=%d\n", result.Feeds)
		return nil
	default:
		return errors.New("usage: paon-admin feeds <build|clear|vacuum> ...")
	}
}

func runCache(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 2 && args[0] == "recount" {
		count, err := operations.RecountCache(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("recounted=%d\n", count)
		return nil
	}
	if len(args) != 2 || args[0] != "clear" || args[1] != "--confirm" {
		return errors.New("usage: paon-admin cache <clear --confirm|recount accounts|recount statuses>")
	}
	deleted, err := operations.ClearCache(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("deleted=%d\n", deleted)
	return nil
}

func randomPassword() string {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func commandFlagArgs(args []string) []string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return args
	}
	reordered := append([]string(nil), args[1:]...)
	return append(reordered, args[0])
}

func usageError() error {
	return errors.New("usage: paon-admin <accounts|settings|domains|email-domain-blocks|canonical-email-blocks|ip-blocks|feeds|cache|vacuum|version> ...")
}
