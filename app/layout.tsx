import type { Metadata, Viewport } from "next";
import { site } from "@/lib/site";
import "./globals.css";

export const metadata: Metadata = {
  metadataBase: new URL(site.domain),
  title: {
    default: "Orbit — Zero-downtime deployments for Docker Compose",
    template: "%s — Orbit",
  },
  description: site.description,
  applicationName: "Orbit",
  keywords: [
    "Docker Compose",
    "zero-downtime deployment",
    "rolling deployment",
    "Docker CLI plugin",
    "blue-green deployment",
    "reverse proxy",
    "DevOps",
    "self-hosting",
    "no Kubernetes",
  ],
  authors: [{ name: "Orbit" }],
  alternates: { canonical: "/" },
  openGraph: {
    type: "website",
    url: site.domain,
    siteName: "Orbit",
    title: "Zero-downtime deployments for Docker Compose",
    description: site.description,
  },
  twitter: {
    card: "summary_large_image",
    title: "Zero-downtime deployments for Docker Compose",
    description: site.description,
  },
  robots: {
    index: true,
    follow: true,
    googleBot: { index: true, follow: true },
  },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
    { media: "(prefers-color-scheme: dark)", color: "#0b0d0e" },
  ],
};

const jsonLd = {
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  name: "Orbit",
  applicationCategory: "DeveloperApplication",
  operatingSystem: "Linux, macOS",
  description: site.description,
  url: site.domain,
  license: "https://opensource.org/licenses/MIT",
  offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
  codeRepository: site.repo,
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        <a href="#main" className="skip-link">
          Skip to content
        </a>
        {children}
        <script
          type="application/ld+json"
          // eslint-disable-next-line react/no-danger
          dangerouslySetInnerHTML={{ __html: JSON.stringify(jsonLd) }}
        />
      </body>
    </html>
  );
}
