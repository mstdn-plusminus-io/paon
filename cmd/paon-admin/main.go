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
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
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
	if err := cfg.ValidateOpenTelemetry(); err != nil {
		return fmt.Errorf("check OpenTelemetry configuration: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if cfg.OpenTelemetryEnabled {
		telemetryRuntime, err := telemetry.Initialize(ctx, telemetry.OptionsFromConfig(cfg, "paon-admin"))
		if err != nil {
			return fmt.Errorf("initialize OpenTelemetry: %w", err)
		}
		defer func() {
			if err := telemetryRuntime.ShutdownWithTimeout(10 * time.Second); err != nil {
				log.Printf("shutdown OpenTelemetry: %v", err)
			}
		}()
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := paondb.SchemaAvailable(database); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}
	operations := api.NewOperations(cfg, database)
	defer operations.Close()

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
	case "emoji":
		return runEmoji(ctx, operations, os.Args[2:])
	case "media":
		return runMedia(ctx, operations, os.Args[2:])
	case "storage-schema":
		return runStorageSchema(ctx, operations, os.Args[2:])
	case "search":
		return runSearch(ctx, operations, os.Args[2:])
	case "self-destruct":
		return runSelfDestruct(ctx, operations, cfg, os.Args[2:], os.Stdin, os.Stdout)
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
			// The password/session phase intentionally commits before OAuth
			// revocation. Preserve operator access to the generated password if
			// that later phase fails and the command must be retried.
			if result.GeneratedPassword != "" {
				fmt.Printf("password=%s\n", result.GeneratedPassword)
			}
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
		entries, err := operations.ListEmailDomainBlockEntries(ctx)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			prefix := ""
			if entry.ParentID.Valid {
				prefix = "  "
			}
			fmt.Printf("%s%s\tallow_with_approval=%t\n", prefix, entry.Domain, entry.AllowWithApproval)
		}
		return nil
	case "add":
		flags := flag.NewFlagSet("email-domain-blocks add", flag.ContinueOnError)
		allowWithApproval := flags.Bool("allow-with-approval", false, "allow registration but require manual approval")
		withDNSRecords := flags.Bool("with-dns-records", false, "also block resolved MX hosts")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() < 1 {
			return errors.New("email-domain-blocks add requires at least one domain")
		}
		added, skipped, err := operations.AddEmailDomainBlocksWithDNS(ctx, flags.Args(), *allowWithApproval, *withDNSRecords)
		if err != nil {
			return err
		}
		fmt.Printf("added=%d skipped=%d\n", added, skipped)
		return nil
	case "remove":
		flags := flag.NewFlagSet("email-domain-blocks remove", flag.ContinueOnError)
		dryRun := flags.Bool("dry-run", false, "show matching blocks without deleting")
		confirm := flags.Bool("confirm", false, "delete matching blocks and their DNS children")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() < 1 || *dryRun == *confirm {
			return errors.New("email-domain-blocks remove requires domains and either --dry-run or --confirm")
		}
		entries, err := operations.EmailDomainBlockRemovalInventory(ctx, flags.Args())
		if err != nil {
			return err
		}
		for _, entry := range entries {
			fmt.Printf("email_domain_block_id=%d domain=%s parent_id=%d\n", entry.ID, entry.Domain, entry.ParentID.Int64)
		}
		if *dryRun {
			fmt.Printf("would_remove=%d\n", len(entries))
			return nil
		}
		removed, err := operations.RemoveEmailDomainBlocks(ctx, flags.Args())
		if err != nil {
			return err
		}
		fmt.Printf("removed=%d\n", removed)
		return nil
	default:
		return errors.New("usage: paon-admin email-domain-blocks <list|add|remove> [DOMAIN...]")
	}
}

func runEmoji(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 || args[0] != "purge" {
		return errors.New("usage: paon-admin emoji purge [--remote-only|--suspended-only] (--dry-run|--confirm)")
	}
	flags := flag.NewFlagSet("emoji purge", flag.ContinueOnError)
	remoteOnly := flags.Bool("remote-only", false, "only remove remote custom emoji")
	suspendedOnly := flags.Bool("suspended-only", false, "only remove emoji from suspended domains")
	dryRun := flags.Bool("dry-run", false, "show the exact purge inventory")
	confirm := flags.Bool("confirm", false, "delete the inventoried emoji and stored objects")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*remoteOnly && *suspendedOnly) || *dryRun == *confirm {
		return errors.New("emoji purge accepts at most one scope and requires --dry-run or --confirm")
	}
	entries, err := operations.CustomEmojiPurgeInventory(ctx, *remoteOnly, *suspendedOnly)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		domain := entry.Domain
		if domain == "" {
			domain = "local"
		}
		fmt.Printf("emoji_id=%d shortcode=%s domain=%s\n", entry.ID, entry.Shortcode, domain)
	}
	if *dryRun {
		fmt.Printf("would_remove=%d\n", len(entries))
		return nil
	}
	removed, err := operations.PurgeCustomEmojis(ctx, *remoteOnly, *suspendedOnly)
	if err != nil {
		return err
	}
	fmt.Printf("removed=%d\n", removed)
	return nil
}

func runMedia(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 || args[0] != "refresh" {
		return errors.New("usage: paon-admin media refresh (--status ID|--account HANDLE|--domain DOMAIN|--days N) [--force] (--dry-run|--confirm)")
	}
	flags := flag.NewFlagSet("media refresh", flag.ContinueOnError)
	statusID := flags.Int64("status", 0, "refresh attachments belonging to a status ID")
	account := flags.String("account", "", "refresh attachments belonging to username@domain")
	domain := flags.String("domain", "", "refresh attachments from a domain and subdomains")
	days := flags.Int("days", 0, "refresh remote attachments created within N days")
	force := flags.Bool("force", false, "re-download attachments that already have a local file")
	dryRun := flags.Bool("dry-run", false, "show attachment IDs without queueing")
	confirm := flags.Bool("confirm", false, "queue or perform the downloads")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dryRun == *confirm {
		return errors.New("media refresh requires one source and either --dry-run or --confirm")
	}
	options := api.OperationMediaRefreshOptions{StatusID: *statusID, Account: *account, Domain: *domain, Days: *days, Force: *force}
	entries, err := operations.MediaRefreshInventory(ctx, options)
	if err != nil {
		return err
	}
	var size int64
	for _, entry := range entries {
		size += entry.Size
		fmt.Printf("media_attachment_id=%d cached_bytes=%d\n", entry.ID, entry.Size)
	}
	if *dryRun {
		fmt.Printf("would_refresh=%d cached_bytes=%d\n", len(entries), size)
		return nil
	}
	processed, cachedBytes, err := operations.RefreshMedia(ctx, options)
	if err != nil {
		return err
	}
	fmt.Printf("refresh_queued=%d cached_bytes=%d\n", processed, cachedBytes)
	return nil
}

func runStorageSchema(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 || (args[0] != "check" && args[0] != "upgrade") {
		return errors.New("usage: paon-admin storage-schema <check|upgrade> [--dry-run|--confirm]")
	}
	inventory, err := operations.StorageSchemaInventory(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("account_avatars=%d account_headers=%d custom_emojis=%d media=%d preview_cards=%d total=%d\n",
		inventory.AccountAvatars, inventory.AccountHeaders, inventory.CustomEmojis, inventory.Media, inventory.PreviewCards, inventory.Total())
	if args[0] == "check" {
		if len(args) != 1 {
			return errors.New("storage-schema check does not accept mutation flags")
		}
		return nil
	}
	flags := flag.NewFlagSet("storage-schema upgrade", flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "validate the upgrade inventory without moving files")
	confirm := flags.Bool("confirm", false, "move objects and persist storage schema version 1")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dryRun == *confirm {
		return errors.New("storage-schema upgrade requires --dry-run or --confirm")
	}
	upgraded, err := operations.UpgradeStorageSchema(ctx, *dryRun)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("would_upgrade=%d\n", upgraded)
	} else {
		fmt.Printf("upgraded=%d\n", upgraded)
	}
	return nil
}

func runSearch(ctx context.Context, operations *api.Operations, args []string) error {
	if len(args) == 0 || args[0] != "deploy" {
		return errors.New("usage: paon-admin search deploy [--batch-size N] [--resume] [--progress-file PATH] (--dry-run|--confirm)")
	}
	flags := flag.NewFlagSet("search deploy", flag.ContinueOnError)
	batchSize := flags.Int("batch-size", 100, "database records per Meilisearch batch")
	resume := flags.Bool("resume", false, "resume from the progress file")
	progressFile := flags.String("progress-file", "tmp/meilisearch_deploy_progress.json", "non-secret deploy checkpoint path")
	dryRun := flags.Bool("dry-run", false, "show database inventory without changing indexes")
	confirm := flags.Bool("confirm", false, "synchronize and populate all Meilisearch indexes")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *batchSize < 1 || *dryRun == *confirm {
		return errors.New("search deploy requires batch-size >= 1 and either --dry-run or --confirm")
	}
	inventory, err := operations.SearchDeployInventory(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("inventory accounts=%d statuses=%d tags=%d instances=%d\n", inventory.Accounts, inventory.Statuses, inventory.Tags, inventory.Instances)
	if *dryRun {
		return nil
	}
	stats, err := operations.DeploySearch(ctx, api.MeiliDeployOptions{BatchSize: *batchSize, Resume: *resume, ProgressPath: *progressFile, Writer: os.Stdout})
	if err != nil {
		return err
	}
	fmt.Printf("indexed accounts=%d statuses=%d tags=%d instances=%d\n", stats.Accounts, stats.Statuses, stats.Tags, stats.Instances)
	return nil
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
	return errors.New("usage: paon-admin <accounts|settings|domains|email-domain-blocks|canonical-email-blocks|ip-blocks|feeds|cache|vacuum|emoji|media|storage-schema|search|self-destruct|version> ...")
}
