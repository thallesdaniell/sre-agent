import { z } from "zod";

/**
 * Inventário: quais produtos o agente pode investigar e ONDE pode olhar.
 * Tudo que não está aqui é recusado antes de qualquer chamada externa.
 */
export const ProductScope = z.object({
  product: z.string().regex(/^[a-z0-9-]+$/),
  namespaces: z.array(z.string().regex(/^[a-z0-9-]+$/)).min(1),
  repository: z.string().regex(/^[\w.-]+\/[\w.-]+$/).describe("owner/repo"),
  environment: z.enum(["production", "staging"]),
});
export type ProductScope = z.infer<typeof ProductScope>;

export class ScopeRegistry {
  private readonly byProduct: Map<string, ProductScope>;

  constructor(scopes: ProductScope[]) {
    this.byProduct = new Map(scopes.map((s) => [s.product, s]));
  }


  products(): string[] {
    return [...this.byProduct.keys()];
  }

  get(product: string): ProductScope {
    const scope = this.byProduct.get(product);
    if (!scope) throw new ScopeError(`Produto fora do inventário: ${product}`);
    return scope;
  }

  /** Resolve o namespace pedido; sem pedido, usa o primeiro do produto. */
  namespace(product: string, requested?: string): string {
    const scope = this.get(product);
    if (requested === undefined) return scope.namespaces[0]!;
    if (!scope.namespaces.includes(requested)) {
      throw new ScopeError(`Namespace ${requested} não pertence a ${product}. Permitidos: ${scope.namespaces.join(", ")}`);
    }
    return requested;
  }
}

/** Erro esperado: volta para o modelo como resultado da tool, para ele corrigir o pedido. */
export class ScopeError extends Error {
  override readonly name = "ScopeError";
}
