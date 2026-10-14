export function Flash({ error, notice }: { error?: string; notice?: string }) {
  return (
    <>
      {error ? (
        <p className="flash flash-error" role="alert">
          {error}
        </p>
      ) : null}
      {notice ? (
        <p className="flash flash-notice" role="status">
          {notice}
        </p>
      ) : null}
    </>
  );
}

export type SearchParams = Promise<Record<string, string | string[] | undefined>>;

export function one(v: string | string[] | undefined): string | undefined {
  return Array.isArray(v) ? v[0] : v;
}
