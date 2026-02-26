import plugin from 'tailwindcss/plugin';

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ['class', 'class'],
  content: ['./src/**/*.{html,js,svelte,ts}'],
  theme: {
    container: {
      center: true,
      padding: '2rem',
      screens: {
        '2xl': '1400px',
      },
    },
    fontFamily: {
      sans: ['system-ui', '-apple-system', '"system-ui"', '"Segoe UI"', 'Roboto', 'sans-serif'],
      mono: ['JetBrains Mono', 'Menlo', 'monospace'],
    },
    extend: {
      fontSize: {
        xs: ['0.75rem', { lineHeight: '1.4', letterSpacing: '0.01em' }],
        sm: ['0.875rem', { lineHeight: '1.5', letterSpacing: '0.01em' }],
        md: ['1rem', { lineHeight: '1.5', letterSpacing: '0' }],
        base: ['1rem', { lineHeight: '1.5', letterSpacing: '0' }],
        lg: ['1.125rem', { lineHeight: '1.5', letterSpacing: '-0.01em' }],
        xl: ['1.25rem', { lineHeight: '1.4', letterSpacing: '-0.02em' }],
        '2xl': ['1.5rem', { lineHeight: '1.3', letterSpacing: '-0.02em' }],
        '3xl': ['1.875rem', { lineHeight: '1.2', letterSpacing: '-0.03em' }],
        '4xl': ['2.25rem', { lineHeight: '1.1', letterSpacing: '-0.03em' }],
        // Consistent UI scale for app components
        10: ['10px', { lineHeight: '1.3', letterSpacing: '0.05em' }], // Labels, section headers
        11: ['11px', { lineHeight: '1.4', letterSpacing: '0.02em' }], // Subtext, footers
        12: ['12px', { lineHeight: '1.4', letterSpacing: '0.01em' }], // Small content
        13: ['13px', { lineHeight: '1.5', letterSpacing: '0' }], // Standard content, input text
        14: ['14px', { lineHeight: '1.5', letterSpacing: '-0.01em' }], // Slightly larger content
        15: ['15px', { lineHeight: '1.5', letterSpacing: '-0.01em' }], // Emphasized content
      },
      colors: {
        gray: {
          850: '#1A1D24',
          900: '#111318',
          950: '#0C0E12',
        },
        linear: {
          border: '#2E323A',
          text: '#ffffff',
          sub: '#8A8F98',
        },
        border: 'hsl(var(--border))',
        input: 'hsl(var(--input))',
        ring: 'hsl(var(--ring))',
        background: 'hsl(var(--background))',
        foreground: 'hsl(var(--foreground))',
        primary: {
          DEFAULT: 'hsl(var(--primary))',
          foreground: 'hsl(var(--primary-foreground))',
        },
        secondary: {
          DEFAULT: 'hsl(var(--secondary))',
          foreground: 'hsl(var(--secondary-foreground))',
        },
        destructive: {
          DEFAULT: 'hsl(var(--destructive))',
          foreground: 'hsl(var(--destructive-foreground))',
        },
        muted: {
          DEFAULT: 'hsl(var(--muted))',
          foreground: 'hsl(var(--muted-foreground))',
        },
        accent: {
          DEFAULT: 'hsl(var(--accent))',
          foreground: 'hsl(var(--accent-foreground))',
        },
        popover: {
          DEFAULT: 'hsl(var(--popover))',
          foreground: 'hsl(var(--popover-foreground))',
        },
        card: {
          DEFAULT: 'hsl(var(--card))',
          foreground: 'hsl(var(--card-foreground))',
        },
        chart: {
          1: 'hsl(var(--chart-1))',
          2: 'hsl(var(--chart-2))',
          3: 'hsl(var(--chart-3))',
          4: 'hsl(var(--chart-4))',
          5: 'hsl(var(--chart-5))',
        },
      },
      borderRadius: {
        lg: 'var(--radius)',
        md: 'calc(var(--radius) - 2px)',
        sm: 'calc(var(--radius) - 4px)',
      },
      keyframes: {
        shimmer: {
          '0%': {
            backgroundPosition: '200% center',
          },
          '100%': {
            backgroundPosition: '-200% center',
          },
        },
        'pulse-glow': {
          '0%, 100%': {
            opacity: '0.5',
            filter: 'drop-shadow(0 0 2px hsl(270 72% 70%))',
          },
          '50%': {
            opacity: '1',
            filter: 'drop-shadow(0 0 8px hsl(270 72% 70%))',
          },
        },
      },
      animation: {
        shimmer: 'shimmer 1.5s linear infinite',
        'pulse-glow': 'pulse-glow 2s ease-in-out infinite',
      },
    },
  },
  plugins: [
    require('tailwindcss-animate'),
    plugin(({ addBase }) => {
      addBase({
        '.text-10, .text-11, .text-12, .text-xs, .text-base': {
          fontWeight: '500',
        },
        '.font-mono.text-10, .font-mono.text-11, .font-mono.text-12, .font-mono.text-xs': {
          fontWeight: '400',
        },
        '.font-mono .text-10, .font-mono .text-11, .font-mono .text-12, .font-mono .text-xs': {
          fontWeight: '400',
        },
      });
    }),
  ],
};
