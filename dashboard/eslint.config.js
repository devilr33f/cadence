import antfu from '@antfu/eslint-config'

export default antfu({
  typescript: true,
  react: true,
  ignores: [
    'src/api/gen',
    'src/routeTree.gen.ts',
  ],
})
