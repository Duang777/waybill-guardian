export const motionDuration = {
  press: 0.12,
  event: 0.18,
  status: 0.24,
} as const;

export const motionEase = [0.16, 1, 0.3, 1] as const;

export const motionTransition = {
  press: {
    duration: motionDuration.press,
    ease: motionEase,
  },
  event: {
    duration: motionDuration.event,
    ease: motionEase,
  },
  status: {
    duration: motionDuration.status,
    ease: motionEase,
  },
} as const;
