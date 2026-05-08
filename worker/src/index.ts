export interface Env {
  BUCKET: R2Bucket;
  ACCESS_TOKEN: string;
}

function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let out = 0;
  for (let i = 0; i < a.length; i++) out |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return out === 0;
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const token = url.searchParams.get("t") ?? "";
    if (!timingSafeEqual(token, env.ACCESS_TOKEN)) {
      return new Response("forbidden", { status: 403 });
    }
    const key = url.pathname.replace(/^\/+/, "");
    if (!key) return new Response("not found", { status: 404 });

    const obj = await env.BUCKET.get(key);
    if (!obj) return new Response("not found", { status: 404 });

    const headers = new Headers();
    obj.writeHttpMetadata(headers);
    headers.set("etag", obj.httpEtag);
    headers.set("cache-control", "private, max-age=3600");
    return new Response(obj.body, { headers });
  },
};
