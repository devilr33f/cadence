import { Toast } from '@base-ui/react/toast'

// toasts are only ever added from event handlers in the browser, so one manager per tab is safe beside ssr
export const toast = Toast.createToastManager()
