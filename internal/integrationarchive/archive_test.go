package integrationarchive

import (
	"bytes"
	"context"
	"testing"

	"prism/internal/testutil"
)

func TestEncryptionAuthenticatesAndRandomizes(t *testing.T) {
	p := []byte(`{"secret":"very sensitive"}`)
	a, err := Seal(p, "long passphrase 123")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(p, "long passphrase 123")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(a, p) || bytes.Equal(a, b) {
		t.Fatal("plaintext exposed or encryption reused randomness")
	}
	got, err := Open(a, "long passphrase 123")
	if err != nil || !bytes.Equal(got, p) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err = Open(a, "wrong passphrase 123"); err == nil {
		t.Fatal("wrong password accepted")
	}
	a[len(a)-1] ^= 1
	if _, err = Open(a, "long passphrase 123"); err == nil {
		t.Fatal("tampering accepted")
	}
}
func TestDatabaseRoundTripIsScopedAndNonDestructive(t *testing.T) {
	ctx := context.Background()
	src := testutil.DB(t)
	dst := testutil.DB(t)
	_, err := src.Exec(ctx, `INSERT INTO providers(name,kind,base_url) VALUES('local','custom','http://localhost'); INSERT INTO mcp_servers(name,transport,headers) VALUES('box','http','{"Authorization":"secret"}'); INSERT INTO settings(key,value) VALUES('telegram','{"enabled":true,"token":"secret"}'); INSERT INTO memory_banks(kind,name,owner) VALUES('project','private','')`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Export(ctx, src.Pool, "long passphrase 123")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(a, []byte("secret")) {
		t.Fatal("credentials appear in ciphertext")
	}
	if err = Import(ctx, dst.Pool, a, "long passphrase 123"); err != nil {
		t.Fatal(err)
	}
	var count int
	err = dst.QueryRow(ctx, `SELECT (SELECT count(*) FROM providers)+(SELECT count(*) FROM mcp_servers)`).Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("integrations missing: %d %v", count, err)
	}
	err = dst.QueryRow(ctx, `SELECT count(*) FROM memory_banks WHERE name='private'`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("memory was included")
	}
	var enabled bool
	if err = dst.QueryRow(ctx, `SELECT (value->>'enabled')::bool FROM settings WHERE key='telegram'`).Scan(&enabled); err != nil || enabled {
		t.Fatal("Telegram should be disabled on import")
	}
	if err = Import(ctx, dst.Pool, a, "long passphrase 123"); err == nil {
		t.Fatal("import replaced existing integration settings")
	}
}
