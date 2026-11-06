// Command prism-integrations moves encrypted integration configuration between PRISM and Facet.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"prism/internal/integrationarchive"
)

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "export" && os.Args[1] != "import") {
		log.Fatal("usage: prism-integrations export|import ARCHIVE (set INTEGRATION_DATABASE_URL and INTEGRATION_ARCHIVE_PASSPHRASE)")
	}
	dsn := os.Getenv("INTEGRATION_DATABASE_URL")
	pass := os.Getenv("INTEGRATION_ARCHIVE_PASSPHRASE")
	if dsn == "" || pass == "" {
		log.Fatal("INTEGRATION_DATABASE_URL and INTEGRATION_ARCHIVE_PASSPHRASE are required")
	}
	ctx := context.Background()
	d, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	switch os.Args[1] {
	case "export":
		data, err := integrationarchive.Export(ctx, d, pass)
		if err != nil {
			log.Fatal(err)
		}
		f, err := os.OpenFile(os.Args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			f.Close()
			log.Fatal(err)
		}
		if err = f.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Encrypted integration archive created.")
	case "import":
		f, err := os.Open(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		info, err := f.Stat()
		if err != nil || info.Size() > 32<<20+1024 {
			log.Fatal("archive is too large or inaccessible")
		}
		data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1025))
		f.Close()
		if err != nil || len(data) > (32<<20)+1024 {
			log.Fatal(err)
		}
		if err = integrationarchive.Import(ctx, d, data, pass); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Integrations imported; Telegram remains disabled.")
	}
}
