import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// styles.css 里自定义的圆角、阴影、字号 token，告诉 tailwind-merge 它们属于哪一类，
// 这样调用方传入 rounded-xl 之类的类名时能正确覆盖组件默认值。
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      rounded: [{ rounded: ['card', 'control'] }],
      shadow: [{ shadow: ['card'] }],
      'font-size': [{ text: ['field'] }],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
