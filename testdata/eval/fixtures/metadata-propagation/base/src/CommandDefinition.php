<?php

final class CommandDefinition
{
    public function __construct(
        public string $name,
        public string $description,
    ) {
    }

    public function toDescriptor(): array
    {
        return [
            'name' => $this->name,
            'description' => $this->description,
        ];
    }
}

final class LazyCommand
{
    public function __construct(private CommandDefinition $command)
    {
    }

    public function descriptor(): array
    {
        return $this->command->toDescriptor();
    }
}
