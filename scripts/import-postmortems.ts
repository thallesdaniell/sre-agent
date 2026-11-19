import { readdirSync, readFileSync } from "node:fs";
import { basename, join } from "node:path";
import { pathToFileURL } from "node:url";
import { z } from "zod";
import { createPool } from "../src/db/pool.js";
import { PgStore } from "../src/store/pg-store.js";
import type { PostmortemInput } from "../src/store/types.js";

/**
 * Importa post-mortems em markdown com um cabeçalho simples:
 *
 *   ---
 *   title: Checkout fora do ar por limite de memória
 *   product: checkout
 *   incident: INC-0981
 *   occurredAt: 2026-08-14
 *   rootCause: Limite de memória reduzido no deploy sem teste de carga
 *   tags: oom, deploy, memoria
 *   ---
 *   (corpo em markdown)
 *
 * O nome do arquivo vira o slug: reimportar o mesmo arquivo atualiza, não duplica.
 * Uso: npm run postmortems:import -- <pasta>
 */
const Frontmatter = z.object({
  title: z.string().min(1),
  product: z.string().regex(/^[a-z0-9-]+$/),
  incident: z.string().optional(),
  occurredAt: z.iso.date().optional(),
  rootCause: z.string().optional(),
  tags: z.string().optional(),
});

export function parsePostmortem(fileName: string, content: string): PostmortemInput {
  const match = /^---\n([\s\S]*?)\n---\n?([\s\S]*)$/.exec(content);
  if (!match) throw new Error(`${fileName}: cabeçalho --- ausente`);
  const fields = Object.fromEntries(
    match[1]!
      .split("\n")
      .filter((line) => line.includes(":"))
      .map((line) => {
        const at = line.indexOf(":");
        return [line.slice(0, at).trim(), line.slice(at + 1).trim()];
      }),
  );
  const meta = Frontmatter.parse(fields);
  return {
    slug: basename(fileName, ".md").toLowerCase().replace(/[^a-z0-9-]/g, "-"),
    product: meta.product,
    title: meta.title,
    incidentRef: meta.incident,
    occurredAt: meta.occurredAt,
    rootCause: meta.rootCause,
    tags: (meta.tags ?? "").split(",").map((t) => t.trim()).filter(Boolean),
    body: match[2]!.trim(),
  };
}

async function main(dir: string): Promise<void> {
  const store = new PgStore(createPool());
  try {
    const files = readdirSync(dir).filter((f) => f.endsWith(".md"));
    for (const file of files) {
      const pm = parsePostmortem(file, readFileSync(join(dir, file), "utf8"));
      await store.upsertPostmortem(pm);
      console.log(`importado: ${pm.slug}`);
    }
    console.log(`${files.length} post-mortem(s)`);
  } finally {
    await store.close();
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const dir = process.argv[2];
  if (!dir) {
    console.error("uso: npm run postmortems:import -- <pasta>");
    process.exit(1);
  }
  main(dir).catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : error);
    process.exit(1);
  });
}
