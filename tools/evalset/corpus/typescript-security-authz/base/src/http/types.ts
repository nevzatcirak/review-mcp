export interface User {
  id: string;
  email: string;
}

export interface Request {
  method: "GET" | "POST";
  path: string;
  params: Record<string, string>;
  /** Set by the session middleware; routes only run for signed-in users. */
  user: User;
}

export interface Response {
  status: number;
  headers?: Record<string, string>;
  body: unknown;
}

export type Handler = (req: Request) => Promise<Response>;

export const ok = (body: unknown, headers?: Record<string, string>): Response => ({ status: 200, body, headers });
export const notFound = (): Response => ({ status: 404, body: { error: "not found" } });
