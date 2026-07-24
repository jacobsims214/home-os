import { Card as MantineCard, CardProps } from "@mantine/core";

export function Card({ children, ...props }: CardProps) {
  return <MantineCard {...props}>{children}</MantineCard>;
}

export default Card;
