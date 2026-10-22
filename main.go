package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const usage = `用法:
  leopic serve                                   启动服务（默认）
  leopic import --lsky-db FILE [--dry-run] [--no-orphans]
                 [--drop-compressed] [--redirect-map FILE]
  leopic regen [--all]                           补生成 / 重建 WebP 衍生图
  leopic token create [NAME]                     新建 API token
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Print(usage)
		return nil
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := openDB(cfg.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	store := &Store{cfg: cfg, db: db}

	switch cmd {
	case "serve":
		if err := cfg.validateServe(); err != nil {
			return err
		}
		return serve(cfg, store)

	case "import":
		fs := flag.NewFlagSet("import", flag.ContinueOnError)
		lsky := fs.String("lsky-db", "", "lsky 的 database.sqlite")
		dry := fs.Bool("dry-run", false, "只检查不写入")
		noOrphans := fs.Bool("no-orphans", false, "不导入库外的孤儿文件")
		drop := fs.Bool("drop-compressed", false, "剔除有原图配对的 -compressed 压缩图（由衍生图替代）")
		mapFile := fs.String("redirect-map", "", "配合 --drop-compressed：输出 nginx map（旧压缩图 URL -> 衍生图 URL）")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *lsky == "" {
			return fmt.Errorf("缺少 --lsky-db")
		}
		if *mapFile != "" && !*drop {
			return fmt.Errorf("--redirect-map 需要与 --drop-compressed 同用")
		}
		st, err := importLsky(store, *lsky, *dry, !*noOrphans, *drop, *mapFile)
		if st != nil {
			fmt.Printf("导入 %d（其中孤儿 %d），剔除压缩图 %d，跳过 %d，缺失 %d，失败 %d，token %d\n",
				st.imported, st.orphans, st.dropped, st.skipped, st.missing, st.failed, st.tokens)
		}
		return err

	case "regen":
		fs := flag.NewFlagSet("regen", flag.ContinueOnError)
		all := fs.Bool("all", false, "全部重建")
		if err := fs.Parse(args); err != nil {
			return err
		}
		n, err := store.regen(*all)
		fmt.Printf("处理 %d 张\n", n)
		return err

	case "token":
		if len(args) == 0 || args[0] != "create" {
			return fmt.Errorf("用法: leopic token create [NAME]")
		}
		name := "cli"
		if len(args) > 1 {
			name = args[1]
		}
		tok, err := createToken(db, name)
		if err != nil {
			return err
		}
		fmt.Println(tok)
		return nil
	}
	fmt.Print(usage)
	return fmt.Errorf("未知命令 %q", cmd)
}

func serve(cfg *Config, store *Store) error {
	for _, d := range []string{cfg.Uploads, cfg.WebpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	s := &Server{cfg: cfg, store: store, limiter: &failLimiter{limit: 5}, adminCookie: randomString(48)}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	fmt.Printf("leopic 监听 %s，上传目录 %s\n", cfg.Addr, cfg.Uploads)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
