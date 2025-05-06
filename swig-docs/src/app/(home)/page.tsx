import Link from 'next/link';

export default function HomePage() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center p-4">
      <div className="flex flex-col items-center space-y-6 text-center">
        <div className="flex items-center space-x-2">
          <h1 className="text-4xl font-bold tracking-tight">Swig</h1>
          <span className="text-3xl">🍺</span>
        </div>
        
        <p className="max-w-2xl text-lg text-fd-muted-foreground">
          A refreshing PostgreSQL-powered job queue for Go applications. 
          Simple, reliable, and built for developers who need robust background job processing.
        </p>

        <div className="flex flex-col space-y-4 sm:flex-row sm:space-x-4 sm:space-y-0">
          <Link
            href="/docs"
            className="rounded-md bg-fd-primary px-4 py-2 text-sm font-medium text-fd-primary-foreground hover:bg-fd-primary/90"
          >
            Get Started
          </Link>
          <Link
            href="https://github.com/glamboyosa/swig"
            target="_blank"
            rel="noopener noreferrer"
            className="rounded-md border border-fd-border px-4 py-2 text-sm font-medium hover:bg-fd-muted"
          >
            View on GitHub
          </Link>
        </div>

        <div className="mt-8 grid grid-cols-1 gap-4 sm:grid-cols-3">
          <div className="rounded-lg border border-fd-border p-4">
            <h3 className="mb-2 font-semibold">PostgreSQL-Powered</h3>
            <p className="text-sm text-fd-muted-foreground">
              Leverages PostgreSQL's native features for reliable job processing
            </p>
          </div>
          <div className="rounded-lg border border-fd-border p-4">
            <h3 className="mb-2 font-semibold">Transaction Support</h3>
            <p className="text-sm text-fd-muted-foreground">
              Seamless integration with your existing database transactions
            </p>
          </div>
          <div className="rounded-lg border border-fd-border p-4">
            <h3 className="mb-2 font-semibold">Simple API</h3>
            <p className="text-sm text-fd-muted-foreground">
              Clean, intuitive interface for job management
            </p>
          </div>
        </div>
      </div>
    </main>
  );
}
