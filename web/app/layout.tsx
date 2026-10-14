import type { Metadata } from "next";
import Link from "next/link";

import { PersonaSwitcher } from "@/components/PersonaSwitcher";

import "./globals.css";

export const metadata: Metadata = {
  title: "dataset-registry",
  description: "Every dataset has an owner, a freshness SLO, a versioned contract, lineage and known consumers.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <a className="skip" href="#main">
          Skip to content
        </a>
        <header className="top">
          <nav aria-label="Main">
            <Link href="/" className="brand">
              dataset-registry
            </Link>
            <Link href="/">Datasets</Link>
            <Link href="/change-requests">Change requests</Link>
            <Link href="/audit">Audit log</Link>
            <Link href="/datasets/new">Register a dataset</Link>
          </nav>
          <PersonaSwitcher />
        </header>
        <main id="main">{children}</main>
        <footer className="foot">
          Lumen &amp; Loom is fictional. Personas use a development token issuer, not real authentication.
        </footer>
      </body>
    </html>
  );
}
