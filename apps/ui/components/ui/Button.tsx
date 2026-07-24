import { Button as MantineButton } from "@mantine/core";
import type { ButtonProps } from "@mantine/core";

// Re-export Mantine Button with variant="filled" as default
// The MantineProvider already sets size="sm" and fw="600" as defaults
export default MantineButton;
export { MantineButton as Button };
export type { ButtonProps };
