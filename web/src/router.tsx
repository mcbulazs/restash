import { useEffect, useState, type AnchorHTMLAttributes } from "react";

// A tiny History API router. The server returns index.html for unknown
// paths, so deep links work on reload.

const listeners = new Set<() => void>();

export function navigate(to: string, replace = false) {
  if (to === location.pathname + location.search) return;
  if (replace) history.replaceState(null, "", to);
  else history.pushState(null, "", to);
  listeners.forEach((l) => l());
  window.scrollTo(0, 0);
}

/** The current pathname; re-renders on any URL change. */
export function usePath(): string {
  const [, setUrl] = useState(location.pathname + location.search);
  useEffect(() => {
    const update = () => setUrl(location.pathname + location.search);
    listeners.add(update);
    window.addEventListener("popstate", update);
    return () => {
      listeners.delete(update);
      window.removeEventListener("popstate", update);
    };
  }, []);
  return location.pathname;
}

export function query(name: string): string {
  return new URLSearchParams(location.search).get(name) ?? "";
}

/** Like <a>, but navigates without a full page load for internal links. */
export function Link({ href, onClick, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string }) {
  return (
    <a
      href={href}
      onClick={(e) => {
        onClick?.(e);
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || !href.startsWith("/")) return;
        e.preventDefault();
        navigate(href);
      }}
      {...rest}
    />
  );
}
