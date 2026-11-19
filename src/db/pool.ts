import pg from "pg";

/**
 * Pool de conexões. O intake fica com poucas conexões abertas; o job abre, usa e fecha.
 * DATABASE_URL no formato postgres://usuario:senha@host:5432/banco
 */
export function createPool(connectionString: string | undefined = process.env["DATABASE_URL"], max = 5): pg.Pool {
  if (!connectionString) throw new Error("DATABASE_URL não definida");
  return new pg.Pool({ connectionString, max, idleTimeoutMillis: 30_000, connectionTimeoutMillis: 5_000 });
}
