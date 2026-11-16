import type { Handler, Request, Response } from "./types.js";
import { notFound } from "./types.js";

interface Route {
  method: Request["method"];
  pattern: RegExp;
  names: string[];
  handler: Handler;
}

export class Router {
  private readonly routes: Route[] = [];

  /** Registers a handler for a path such as /orders/:id. */
  add(method: Request["method"], path: string, handler: Handler): void {
    const names: string[] = [];
    const source = path.replace(/:([a-zA-Z]+)/g, (_, name: string) => {
      names.push(name);
      return "([^/]+)";
    });
    this.routes.push({ method, pattern: new RegExp(`^${source}$`), names, handler });
  }

  async handle(req: Omit<Request, "params">): Promise<Response> {
    for (const route of this.routes) {
      const match = route.method === req.method ? route.pattern.exec(req.path) : null;
      if (match) {
        const params = Object.fromEntries(route.names.map((name, i) => [name, decodeURIComponent(match[i + 1])]));
        return route.handler({ ...req, params });
      }
    }
    return notFound();
  }
}
