import './global.css';
import { RootProvider } from 'fumadocs-ui/provider';
import { Inter } from 'next/font/google';
import type { ReactNode } from 'react';
import { Analytics } from "@vercel/analytics/react"
import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'Swig Documentation',
  description: 'Comprehensive documentation for Swig, a background job processing library.',
  openGraph: {
    title: 'Swig Documentation',
    description: 'Comprehensive documentation for Swig, a background job processing library.',
    url: 'https://swig.glamboyosa.xyz',
    siteName: 'Swig Docs',
    images: [
      {
        url: '/api/og',
        width: 1200,
        height: 630,
        alt: 'Swig Documentation Open Graph Image',
      },
    ],
    type: 'website',
  },
  twitter: {
    card: 'summary_large_image',
    title: 'Swig Documentation',
    description: 'Comprehensive documentation for Swig, a background job processing library.',
    // site: '@yourTwitterHandle', // Replace with your site's Twitter handle
    // creator: '@yourPersonalTwitterHandle', // Replace with your personal Twitter handle
    images: ['/api/og'], // Relative path to the OG image route
  },
  // You can add more metadata here, like icons, manifest, etc.
};

const inter = Inter({
  subsets: ['latin'],
});

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className={inter.className} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <RootProvider
          search={{
            links: [
              ['Home', '/'],
              ['Docs', '/docs'],
              ['Installation', '/docs/installation'],
              ['Configuration', '/docs/configuration'],
              ['Examples', '/docs/examples'],
              ['API Reference', '/docs/api'],
            ],
          }}
        >
          {children}
          <Analytics />
        </RootProvider>
      </body>
    </html>
  );
}
