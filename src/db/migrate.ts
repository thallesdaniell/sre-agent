import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import type pg from "pg";
import { createPool } from "./pool.js";

const MIGRATIONS_DIR = fileURLToPath(new URL("../../migrations", import.meta.url));
const LOCK_ID = 74_210_001; // advisory lock: duas réplicas subindo juntas não migram em paralelo

/**
 * Runner mínimo de migrations: aplica, em ordem, os arquivos .sql de migrations/ que ainda não
 * constam em schema_migrations. Cada arquivo roda numa transação; se falhar, nada dele fica.
 * Migration aplicada nunca é editada: mudança nova = arquivo novo.
 */
export async function migrate(pool: pg.Pool, dir: string = MIGRATIONS_DIR): Promise<string[]> {
  const client = await pool.connect();
  try {
    await client.query("SELECT pg_advisory_lock($1)", [LOCK_ID]);
    await client.query(`CREATE TABLE IF NOT EXISTS schema_migrations (
      name text PRIMARY KEY,
      applied_at timestamptz NOT NULL DEFAULT now()
    )`);
    const applied = new Set((await client.query<{ name: string }>("SELECT name FROM schema_migrations")).rows.map((r) => r.name));
    const pending = readdirSync(dir).filter((f) => f.endsWith(".sql") && !applied.has(f)).sort();

    for (const file of pending) {
      await client.query("BEGIN");
      try {
        await client.query(readFileSync(join(dir, file), "utf8"));
        await client.query("INSERT INTO schema_migrations (name) VALUES ($1)", [file]);
        await client.query("COMMIT");
      } catch (error) {
        await client.query("ROLLBACK");
        throw new Error(`Migration ${file} falhou: ${error instanceof Error ? error.message : String(error)}`);
      }
    }
    return pending;
  } finally {
    await client.query("SELECT pg_advisory_unlock($1)", [LOCK_ID]).catch(() => undefined);
    client.release();
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const pool = createPool();
  migrate(pool)
    .then((applied) => console.log(applied.length ? `aplicadas: ${applied.join(", ")}` : "nada a aplicar"))
    .catch((error: unknown) => {
      console.error(error instanceof Error ? error.message : error);
      process.exitCode = 1;
    })
    .finally(() => pool.end());
}
