module.exports = {
  darkMode: 'class',
  content: ["./dashboard/dist/index.html"],
  theme: {
    extend: {
      colors: {
        brand: {
          DEFAULT: '#4f46e5',
          dark: '#3730a3',
          glow: 'rgba(79, 70, 229, 0.15)'
        },
        darkbg: '#090a0f',
        cardbg: 'rgba(15, 17, 26, 0.7)'
      }
    }
  },
  plugins: [],
}
