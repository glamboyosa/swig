import { ImageResponse } from 'next/og';

// App router includes @vercel/og.
// No need to install it.

export const runtime = 'edge'; // Optional: Enables Edge Functions for faster response

export async function GET() {
  return new ImageResponse(
    (
      <div
        style={{
          fontSize: 80, // Made text larger
          color: 'black',
          background: 'white',
          width: '100%',
          height: '100%',
          padding: '50px',
          display: 'flex',
          textAlign: 'center',
          justifyContent: 'center',
          alignItems: 'center',
          fontFamily: 'sans-serif',
          font: "Inter", 
        }}
      >
        Swig 🍺: Background Jobs Made Easy
      </div>
    ),
    {
      width: 1200,
      height: 630,
    },
  );
} 