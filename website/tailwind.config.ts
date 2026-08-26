import type { Config } from 'tailwindcss'

export default {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: {
    extend: {
      colors: {
        cats: {
          50: '#F0FAF6',
          100: '#DDF4EB',
          200: '#BDE8D8',
          500: '#1A9D7A',
          600: '#148363',
          700: '#11745B',
          900: '#152F28',
        },
        ink: '#15231F',
        canvas: '#F8F8F8',
      },
      boxShadow: {
        soft: '0 24px 70px rgba(24, 57, 47, 0.10)',
        card: '0 12px 35px rgba(24, 57, 47, 0.07)',
      },
      fontFamily: {
        sans: ['Inter', 'Noto Sans SC', 'PingFang SC', 'Microsoft YaHei', 'sans-serif'],
      },
    },
  },
  plugins: [],
} satisfies Config
