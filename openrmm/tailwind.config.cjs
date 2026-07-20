module.exports = {
  content: ['./index.html', './src/**/*.{js,jsx}'],
  safelist: [
    'text-ok', 'text-warn', 'text-run', 'text-bad', 'text-muted',
    'fill-ok', 'fill-warn', 'fill-bad',
  ],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        app: 'var(--app)', panel: 'var(--panel)', card: 'var(--card)', elevated: 'var(--elevated)',
        line: 'var(--line)', line2: 'var(--line2)', ink: 'var(--ink)', muted: 'var(--muted)', faint: 'var(--faint)',
        brand: 'var(--brand)', brandhi: 'var(--brandhi)', brandsoft: 'var(--brandsoft)',
        ok: 'var(--ok)', oksoft: 'var(--oksoft)', warn: 'var(--warn)', warnsoft: 'var(--warnsoft)',
        run: 'var(--run)', runsoft: 'var(--runsoft)', bad: 'var(--bad)', badsoft: 'var(--badsoft)',
      },
      fontFamily: {
        sans: ['Inter Variable', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono Variable', 'ui-monospace', 'monospace'],
      },
      borderRadius: { xl: '10px', '2xl': '14px' },
      keyframes: {
        'fade-in': { '0%': { transform: 'translateY(5px)' }, '100%': { transform: 'none' } },
        'slide-in': { '0%': { transform: 'translateX(40px)' }, '100%': { transform: 'none' } },
        'scale-in': { '0%': { transform: 'scale(.97)' }, '100%': { transform: 'none' } },
        'pulse-ring': { '0%': { boxShadow: '0 0 0 0 var(--ringcolor)' }, '70%': { boxShadow: '0 0 0 6px transparent' }, '100%': { boxShadow: '0 0 0 0 transparent' } },
        shimmer: { '100%': { transform: 'translateX(100%)' } },
      },
      animation: {
        'fade-in': 'fade-in .22s ease both',
        'slide-in': 'slide-in .28s cubic-bezier(.32,.72,0,1)',
        'scale-in': 'scale-in .16s ease both',
        'pulse-ring': 'pulse-ring 1.8s ease-out infinite',
      },
    },
  },
};
